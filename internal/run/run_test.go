package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owcli/internal/claims"
	"owcli/internal/okf"
	"owcli/internal/store"
)

type fixture struct {
	t     *testing.T
	repo  string
	l     store.Layout
	clock time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	f := &fixture{t: t, repo: repo, clock: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	f.write("greet.go", "package greet\n\n// Greet prints hello.\nfunc Greet() { println(\"hello\") }\n")
	f.write("README.md", "# Greeter\n")
	f.git("init", "-q")
	f.commit("initial")
	root, _ := filepath.EvalSymlinks(repo)
	f.repo = root
	f.l = store.Layout{Kind: store.External, RepoRoot: root, WikiRoot: filepath.Join(base, "data", "openwiki")}
	return f
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", f.repo}, args...)...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (f *fixture) commit(msg string) {
	f.git("add", "-A")
	f.git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", msg)
}

func (f *fixture) env() Env {
	return Env{Layout: f.l, Producer: "owcli/test", Model: "test-model", Now: func() time.Time { f.clock = f.clock.Add(time.Second); return f.clock }}
}

func (f *fixture) begin(mode Mode) (*Run, BeginResult) {
	f.t.Helper()
	r, res, err := Begin(f.env(), mode, "")
	if err != nil {
		f.t.Fatal(err)
	}
	return r, res
}

// page writes a wiki page as a worker would.
func (f *fixture) page(page, body string) {
	f.t.Helper()
	p := filepath.Join(f.l.WikiRoot, filepath.FromSlash(strings.TrimPrefix(page, "/openwiki/")))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(rel string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.l.WikiRoot, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

const (
	greetPage = "/openwiki/concepts/greet.md"
	greetBody = "---\ntype: concept\ntitle: Greeting\ndescription: How greeting works.\n---\n\n# Greeting\n\n`Greet` prints hello. See [quickstart](../quickstart.md).\n"
	quickBody = "---\ntype: guide\ntitle: Quickstart\ndescription: Start here.\n---\n\n# Quickstart\n\nRead [Greeting](concepts/greet.md).\n"
)

var greetClaim = claims.Proposal{Claims: []claims.ProposedClaim{{Statement: "Greet prints hello.", Evidence: []string{"repo://greet.go#L3-L4"}}}}
var quickClaim = claims.Proposal{Claims: []claims.ProposedClaim{{Statement: "The project is called Greeter.", Evidence: []string{"repo://README.md"}}}}

// work completes the current job with body and proposal.
func (f *fixture) work(r *Run, body string, p claims.Proposal) {
	f.t.Helper()
	next, err := r.Next()
	if err != nil || next == nil {
		f.t.Fatalf("next: %+v %v", next, err)
	}
	f.page(next.Page, body)
	if _, err := r.SubmitPage(next.ID, p); err != nil {
		f.t.Fatalf("submit %s: %v", next.Page, err)
	}
}

func (f *fixture) initWiki() {
	f.t.Helper()
	r, _ := f.begin(Init)
	if err := r.SubmitPlan(PlanInput{Pages: []PlannedPage{{Path: "quickstart.md"}, {Path: "concepts/greet.md"}}}); err != nil {
		f.t.Fatal(err)
	}
	f.work(r, greetBody, greetClaim)
	f.work(r, quickBody, quickClaim)
	res, err := r.Finish(nil)
	if err != nil {
		f.t.Fatal(err)
	}
	if res.Status != store.StatusComplete || res.SourceChanged {
		f.t.Fatalf("finish %+v", res)
	}
}

func TestInitEndToEnd(t *testing.T) {
	f := newFixture(t)
	f.initWiki()

	if _, err := os.Stat(f.l.RunPath()); !os.IsNotExist(err) {
		t.Error("checkpoint must be removed after finish")
	}
	lu, err := f.l.LoadLastUpdate()
	if err != nil || lu.Status != store.StatusComplete || lu.Command != "init" || lu.Model != "test-model" {
		t.Fatalf("last update %+v %v", lu, err)
	}
	m, _ := f.l.LoadManifest()
	if len(m.Pages) != 2 || m.Pages[greetPage].CompletedBy != "owcli/test" {
		t.Fatalf("manifest %+v", m)
	}
	greet := f.read("concepts/greet.md")
	for _, want := range []string{"generated: { by: \"owcli/test\"", "verified:\n  - by: owcli/test", "sources:\n  - id: " + claims.SourceID("repo://greet.go")} {
		if !strings.Contains(greet, want) {
			t.Errorf("greet page lacks %q:\n%s", want, greet)
		}
	}
	if okf.Validate(greet) != nil {
		t.Error("page must be OKF-valid")
	}
	if !strings.Contains(f.read("index.md"), "okf_version") || !strings.Contains(f.read("concepts/index.md"), "[Greeting](greet.md)") {
		t.Error("indexes not synchronized")
	}
	if !strings.Contains(f.read("INSTRUCTIONS.md"), "Wiki Instructions") {
		t.Error("default instructions not written")
	}
	// Every page's sidecar matches its final bytes.
	for _, p := range []string{greetPage, "/openwiki/quickstart.md"} {
		pc, err := claims.NewStore(f.l).Load(p)
		if err != nil || pc == nil {
			t.Fatalf("sidecar %s: %v", p, err)
		}
		if hash, _ := claims.NewStore(f.l).HashPage(p); hash != pc.PageVersion || m.Pages[p].PageVersion != hash {
			t.Errorf("%s: sidecar/manifest page versions out of date", p)
		}
	}
	// The external layout wrote nothing into the repository.
	if status := f.git("status", "--porcelain", "--ignored"); status != "" {
		t.Errorf("repository changed:\n%s", status)
	}

	// A clean update has nothing to do.
	if _, res := f.begin(Update); !res.Noop {
		t.Errorf("expected noop, got %+v", res)
	}
}

func TestUpdateReconcilesStaleClaims(t *testing.T) {
	f := newFixture(t)
	f.initWiki()
	f.write("greet.go", "package greet\n\n// Greet prints hi.\nfunc Greet() { println(\"hi\") }\n")
	f.commit("say hi")

	r, res := f.begin(Update)
	if res.Noop || len(res.Issues) != 1 || res.Issues[0].Kind != claims.Stale {
		t.Fatalf("begin %+v", res)
	}
	// The planner only mentions the quickstart; the stale page is added.
	if err := r.SubmitPlan(PlanInput{Pages: []PlannedPage{{Path: "quickstart.md"}}}); err != nil {
		t.Fatal(err)
	}
	next, _ := r.Next()
	if next.Page != greetPage || next.Reason == "" || len(next.ClaimsRequiringAttention) != 1 || !next.Existing || next.ExistingClaimCount != 1 {
		t.Fatalf("next %+v", next)
	}
	id := next.ClaimsRequiringAttention[0].ID
	f.page(greetPage, strings.Replace(greetBody, "prints hello", "prints hi", 1))
	if _, err := r.SubmitPage(next.ID, claims.Proposal{}); !IsCode(err, InvalidInput) {
		t.Fatalf("omitting a stale claim must be rejected: %v", err)
	}
	if _, err := r.SubmitPage(next.ID, claims.Proposal{Claims: []claims.ProposedClaim{{ID: id, Statement: "Greet prints hi.", Evidence: []string{"repo://greet.go#L3-L4"}}}}); err != nil {
		t.Fatal(err)
	}
	f.work(r, quickBody, claims.Proposal{}) // unchanged quickstart claims are kept
	fin, err := r.Finish(nil)
	if err != nil || fin.Status != store.StatusComplete {
		t.Fatalf("finish %+v %v", fin, err)
	}
	m, _ := f.l.LoadManifest()
	if m.Pages[greetPage].GitHead != strings.TrimSpace(f.git("rev-parse", "HEAD")) {
		t.Error("regenerated page should carry the new source checkpoint")
	}
	if _, res := f.begin(Update); !res.Noop {
		t.Errorf("expected noop after reconciling, got %+v", res)
	}
}

func TestResumeAfterCrashAndSkip(t *testing.T) {
	f := newFixture(t)
	r, _ := f.begin(Init)
	if err := r.SubmitPlan(PlanInput{Pages: []PlannedPage{{Path: "quickstart.md"}, {Path: "concepts/greet.md"}, {Path: "concepts/extra.md"}}}); err != nil {
		t.Fatal(err)
	}
	f.work(r, greetBody, greetClaim) // concepts/extra.md comes before greet alphabetically
	// Process dies here; a new process resumes.
	r2, res := f.begin(Init)
	if !res.Resumed || res.Phase != Generating {
		t.Fatalf("resume %+v", res)
	}
	next, _ := r2.Next()
	if next.Page != greetPage || next.Remaining != 2 {
		t.Fatalf("next after resume %+v", next)
	}
	// The worker writes something and then fails: skip restores the page.
	snap, err := r2.Snapshot(next.ID)
	if err != nil || snap.Markdown != nil {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
	f.page(greetPage, "partial")
	if err := r2.Skip(snap); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.l.WikiRoot, "concepts", "greet.md")); !os.IsNotExist(err) {
		t.Error("skipped page should be removed")
	}
	f.work(r2, quickBody, quickClaim)
	if _, err := r2.Finish(nil); !IsCode(err, InvalidState) {
		t.Fatalf("finish without the skip snapshot must fail: %v", err)
	}
	fin, err := r2.Finish([]PageSnapshot{snap})
	if err != nil || fin.Status != store.StatusInterrupted || len(fin.Skipped) != 1 {
		t.Fatalf("finish %+v %v", fin, err)
	}
	if lu, _ := f.l.LoadLastUpdate(); lu.Status != store.StatusInterrupted {
		t.Error("a skipped page leaves the wiki interrupted")
	}
	if _, res := f.begin(Update); res.Noop {
		t.Error("an interrupted wiki is never a noop update")
	}
}

func TestResumeResetsSkippedAndDetectsDrift(t *testing.T) {
	f := newFixture(t)
	r, _ := f.begin(Init)
	if err := r.SubmitPlan(PlanInput{Pages: []PlannedPage{{Path: "quickstart.md"}, {Path: "concepts/greet.md"}}}); err != nil {
		t.Fatal(err)
	}
	next, _ := r.Next()
	snap, _ := r.Snapshot(next.ID)
	if err := r.Skip(snap); err != nil {
		t.Fatal(err)
	}
	r2, res := f.begin(Init)
	if n, _ := r2.Next(); res.PlanInvalidated || n.ID != next.ID {
		t.Fatalf("skipped job should be pending again: %+v %+v", res, n)
	}
	if _, _, err := Begin(f.env(), Update, ""); !IsCode(err, Conflict) {
		t.Fatalf("mode conflict: %v", err)
	}
	f.write("new.go", "package greet\n")
	_, res = f.begin(Init)
	if !res.PlanInvalidated || res.Phase != Planning {
		t.Fatalf("source drift must invalidate the plan: %+v", res)
	}
}

func TestPlanValidation(t *testing.T) {
	f := newFixture(t)
	r, _ := f.begin(Init)
	bad := []PlanInput{
		{}, // init needs pages
		{Pages: []PlannedPage{{Path: "concepts/x.md"}}},
		{Pages: []PlannedPage{{Path: "quickstart.md"}, {Path: "index.md"}}},
		{Pages: []PlannedPage{{Path: "quickstart.md"}, {Path: "../x.md"}}},
		{Pages: []PlannedPage{{Path: "quickstart.md"}}, Deletions: []string{"concepts/old.md"}},
	}
	for i, p := range bad {
		if err := r.SubmitPlan(p); !IsCode(err, InvalidInput) {
			t.Errorf("case %d: want InvalidInput, got %v", i, err)
		}
	}
	plan := PlanInput{Pages: []PlannedPage{{Path: "openwiki/quickstart.md"}, {Path: "concepts/b.md", Title: "B"}, {Path: "concepts/a.md"}, {Path: "/openwiki/concepts/a.md"}}}
	if err := r.SubmitPlan(plan); err != nil {
		t.Fatal(err)
	}
	var pages []string
	for _, j := range r.State().Plan.Jobs {
		pages = append(pages, j.Page)
	}
	if strings.Join(pages, ",") != "/openwiki/concepts/a.md,/openwiki/concepts/b.md,/openwiki/quickstart.md" {
		t.Errorf("order %v", pages)
	}
	if err := r.SubmitPlan(plan); err != nil {
		t.Errorf("resubmitting the same plan is a no-op: %v", err)
	}
	if err := r.SubmitPlan(PlanInput{Pages: []PlannedPage{{Path: "quickstart.md"}}}); !IsCode(err, InvalidState) {
		t.Errorf("a different plan must be rejected: %v", err)
	}
	next, _ := r.Next()
	other := r.State().Plan.Jobs[1].ID
	if _, err := r.SubmitPage(other, claims.Proposal{}); !IsCode(err, InvalidState) {
		t.Errorf("only the current job may be submitted: %v", err)
	}
	if _, err := r.SubmitPage("nope", claims.Proposal{}); !IsCode(err, NotFound) {
		t.Errorf("unknown job: %v", err)
	}
	if _, err := r.SubmitPage(next.ID, greetClaim); !IsCode(err, InvalidInput) {
		t.Errorf("submitting an unwritten page: %v", err)
	}
	if _, err := r.Finish(nil); !IsCode(err, InvalidState) {
		t.Errorf("finish with pending jobs: %v", err)
	}
}

func TestInitReplacesWikiAndDeletesAbandonedPages(t *testing.T) {
	f := newFixture(t)
	f.initWiki()
	if err := os.WriteFile(f.l.InstructionsPath(), []byte("custom instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, _ := f.begin(Init)
	if _, err := os.Stat(filepath.Join(f.l.WikiRoot, "concepts", "greet.md")); !os.IsNotExist(err) {
		t.Error("init should start from a blank wiki")
	}
	if f.read("INSTRUCTIONS.md") != "custom instructions\n" {
		t.Error("init must keep the user's instructions")
	}
	if err := r.SubmitPlan(PlanInput{Pages: []PlannedPage{{Path: "quickstart.md"}}}); err != nil {
		t.Fatal(err)
	}
	f.page("/openwiki/stray.md", "---\ntype: t\n---\n# Stray\n") // written by a worker outside the plan
	f.work(r, quickBody, quickClaim)
	fin, err := r.Finish(nil)
	if err != nil || len(fin.Deleted) != 1 || fin.Deleted[0] != "openwiki/stray.md" {
		t.Fatalf("finish %+v %v", fin, err)
	}
	m, _ := f.l.LoadManifest()
	if len(m.Pages) != 1 {
		t.Errorf("manifest %+v", m)
	}
}

func TestUpdateRequiresWiki(t *testing.T) {
	f := newFixture(t)
	if _, _, err := Begin(f.env(), Update, ""); !IsCode(err, InvalidState) {
		t.Fatalf("update without a wiki: %v", err)
	}
}

func TestFingerprintAndClean(t *testing.T) {
	f := newFixture(t)
	r, _ := f.begin(Init)
	fp1, _ := fingerprint(f.repo, r.ignore)
	f.write("openwiki/ignored.md", "x") // the repository's own openwiki/ is not source
	if fp2, _ := fingerprint(f.repo, r.ignore); fp2 != fp1 {
		t.Error("wiki paths must not affect the fingerprint")
	}
	if clean, _ := worktreeClean(f.repo, r.ignore); !clean {
		t.Error("wiki-only changes keep the tree clean")
	}
	f.write("greet.go", "package greet // changed\n")
	if fp3, _ := fingerprint(f.repo, r.ignore); fp3 == fp1 {
		t.Error("content changes must change the fingerprint")
	}
	if clean, _ := worktreeClean(f.repo, r.ignore); clean {
		t.Error("source changes make the tree dirty")
	}
}
