package claims

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owcli/internal/evidence"
	"owcli/internal/ignore"
	"owcli/internal/okf"
	"owcli/internal/store"
)

const producer = "owcli/test"

type env struct {
	t        *testing.T
	repo     string
	layout   store.Layout
	st       *Store
	resolver evidence.Resolver
	ids      int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	wiki := filepath.Join(base, "wiki", "openwiki")
	for _, d := range []string{repo, wiki} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r, err := evidence.NewRepoResolver(repo, ignore.Parse(""))
	if err != nil {
		t.Fatal(err)
	}
	l := store.Layout{Kind: store.External, RepoRoot: repo, WikiRoot: wiki}
	return &env{t: t, repo: repo, layout: l, st: NewStore(l), resolver: r}
}

func (e *env) source(rel, content string) {
	e.t.Helper()
	p := filepath.Join(e.repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) page(page, content string) {
	e.t.Helper()
	p := filepath.Join(e.layout.WikiRoot, filepath.FromSlash(pageRel(page)))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) newID() string {
	e.ids++
	return fmt.Sprintf("claim_%032d", e.ids)
}

func (e *env) session(persisted map[string]*PageClaims, issues []Issue, orphans []string) *Session {
	e.t.Helper()
	s, err := NewSession(e.resolver, persisted, issues, orphans, e.newID)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) runtime(fresh bool) *Runtime {
	e.t.Helper()
	rt, err := Prepare(e.st, e.resolver, producer, fresh)
	if err != nil {
		e.t.Fatal(err)
	}
	rt.Session.newID = e.newID
	return rt
}

func str(s string) *string { return &s }

const page = "/openwiki/concepts/greet.md"

const pageContent = "---\ntype: concept\ntitle: Greeting\n---\n\n# Greeting\n\nGreet prints hello.\n"

func TestPaths(t *testing.T) {
	good := map[string]string{
		"/openwiki/a.md":         "/openwiki/a.md",
		"openwiki/x/y.md":        "/openwiki/x/y.md",
		`\openwiki\x\y.md`:       "/openwiki/x/y.md",
		" /openwiki//b/../c.md ": "",
	}
	for in, want := range good {
		got, err := NormalizePage(in)
		if want == "" {
			if err == nil {
				t.Errorf("NormalizePage(%q) should reject traversal", in)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("NormalizePage(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"/openwiki/index.md", "/openwiki/sub/INDEX.md", "/openwiki/INSTRUCTIONS.md", "/openwiki/log.md", "/openwiki/.claims/a.md", "/openwiki/a.txt", "/other/a.md", "/openwiki"} {
		if _, err := NormalizePage(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("NormalizePage(%q) should fail", bad)
		}
	}
	for in, want := range map[string]string{"concepts/x.md": "/openwiki/concepts/x.md", "openwiki/x.md": "/openwiki/x.md", "/openwiki/x.md": "/openwiki/x.md"} {
		if got, err := NormalizeToolPage(in); err != nil || got != want {
			t.Errorf("NormalizeToolPage(%q) = %q, %v", in, got, err)
		}
	}
	if sidecarRel("/openwiki/a/b.md") != "a/b.json" {
		t.Error("sidecarRel")
	}
}

func TestApplyIsAtomic(t *testing.T) {
	e := newEnv(t)
	e.source("a.go", "package a\n\nfunc A() {}\n")
	claims, err := Apply(nil, []Operation{{Op: OpAdd, Statement: str(" A exists. "), Evidence: []string{"repo://a.go#L3"}}}, e.resolver, e.newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Statement != "A exists." || claims[0].Evidence[0].Resource != "repo://a.go#L3-L3" {
		t.Fatalf("got %+v", claims)
	}
	id := claims[0].ID

	failing := [][]Operation{
		{{Op: OpConfirm, ID: "claim_unknown"}},
		{{Op: OpConfirm, ID: id}, {Op: OpRetract, ID: id}},
		{{Op: OpAdd, Statement: str("x"), Evidence: []string{"repo://missing.go"}}},
		{{Op: OpAdd, Statement: str("x"), Evidence: []string{"repo://a.go", "repo://a.go"}}},
		{{Op: OpAdd, Statement: str("x"), Evidence: []string{"repo://a.go", "repo://./a.go"}}},
		{{Op: OpAdd, Statement: str("x"), Evidence: []string{"src/a.go"}}},
		{{Op: OpAdd, Statement: str("  "), Evidence: []string{"repo://a.go"}}},
		{{Op: OpAdd, Statement: str("x"), Evidence: []string{}}},
		{{Op: OpUpdate, ID: id}},
		{},
		// A valid op followed by an invalid one must not apply either.
		{{Op: OpRetract, ID: id}, {Op: OpAdd, Statement: str("y"), Evidence: []string{"repo://nope"}}},
	}
	for i, ops := range failing {
		out, err := Apply(claims, ops, e.resolver, e.newID)
		if !errors.Is(err, ErrInvalid) || out != nil {
			t.Errorf("case %d: want ErrInvalid, got %v, %v", i, out, err)
		}
	}
	if len(claims) != 1 || claims[0].ID != id {
		t.Fatal("input claims must not be modified")
	}

	updated, err := Apply(claims, []Operation{{Op: OpUpdate, ID: id, Statement: str("A is a function.")}}, e.resolver, e.newID)
	if err != nil || updated[0].Statement != "A is a function." || updated[0].Evidence[0] != claims[0].Evidence[0] {
		t.Fatalf("update: %+v %v", updated, err)
	}
	gone, err := Apply(updated, []Operation{{Op: OpRetract, ID: id}}, e.resolver, e.newID)
	if err != nil || len(gone) != 0 {
		t.Fatalf("retract: %+v %v", gone, err)
	}
}

func TestSessionOwnershipAcrossPages(t *testing.T) {
	e := newEnv(t)
	e.source("a.go", "package a\n")
	ev := []evidence.Evidence{{Resource: "repo://a.go", Version: "v"}}
	persisted := map[string]*PageClaims{
		"/openwiki/one.md": {Claims: []Claim{{ID: "claim_x", Statement: "s", Evidence: ev}}},
		"/openwiki/two.md": {Claims: []Claim{{ID: "claim_x", Statement: "s", Evidence: ev}}},
	}
	if _, err := NewSession(e.resolver, persisted, nil, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate id across pages: %v", err)
	}
	s := e.session(nil, nil, nil)
	res, err := s.Mutate("/openwiki/one.md", []Operation{
		{Op: OpAdd, Statement: str("first"), Evidence: []string{"repo://a.go"}},
		{Op: OpAdd, Statement: str("second"), Evidence: []string{"repo://a.go"}},
	})
	if err != nil || len(res) != 2 || res[0].ID == res[1].ID || !s.HasClaim(res[1].ID) {
		t.Fatalf("results %+v %v", res, err)
	}
	if _, err := s.Mutate("/openwiki/one.md", []Operation{{Op: OpRetract, ID: res[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if s.HasClaim(res[0].ID) {
		t.Error("retracted id should be released")
	}
}

// grounded creates a page with one finalized claim and returns its runtime.
func grounded(t *testing.T, e *env) (*Runtime, string) {
	t.Helper()
	e.source("greet.go", "package greet\n\n// Greet prints hello.\nfunc Greet() { println(\"hello\") }\n")
	e.page(page, pageContent)
	rt := e.runtime(true)
	if err := Reconcile(rt.Session, page, Proposal{Claims: []ProposedClaim{{Statement: "Greet prints hello.", Evidence: []string{"repo://greet.go#L3-L4"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Finalize("2026-10-01T00:00:00.000Z", nil); err != nil {
		t.Fatal(err)
	}
	claims, _ := rt.Session.Inspect(page)
	return rt, claims[0].ID
}

func TestFinalizePersistsAndProjects(t *testing.T) {
	e := newEnv(t)
	rt, id := grounded(t, e)
	if err := rt.AssertPageDurable(page); err != nil {
		t.Fatal(err)
	}
	if err := rt.AssertWikiDurable(nil); err != nil {
		t.Fatal(err)
	}
	content, _ := e.st.ReadMarkdown(page)
	if issues := okf.Validate(content); issues != nil {
		t.Fatalf("page invalid after projection: %v\n%s", issues, content)
	}
	fields, _ := okf.Fields(content)
	verified := fields["verified"].([]any)[0].(map[string]any)
	if verified["by"] != producer || verified["at"] != "2026-10-01T00:00:00.000Z" {
		t.Errorf("verified = %v", verified)
	}
	pc, err := e.st.Load(page)
	if err != nil || pc.Claims[0].ID != id || pc.PageVersion != HashContent(content) {
		t.Fatalf("sidecar %+v %v", pc, err)
	}
	data, _ := os.ReadFile(filepath.Join(e.layout.WikiRoot, ".claims", "concepts", "greet.json"))
	if !strings.HasPrefix(string(data), "{\n  \"schemaVersion\": 1,\n  \"pageVersion\": \"sha256:") {
		t.Errorf("sidecar layout:\n%s", data)
	}

	// Editing the page breaks the durability proof.
	e.page(page, content+"\nMore.\n")
	if err := rt.AssertPageDurable(page); !errors.Is(err, ErrPersistence) {
		t.Errorf("want durability failure after edit, got %v", err)
	}
}

func TestPreflightAndReconcileIssues(t *testing.T) {
	e := newEnv(t)
	_, id := grounded(t, e)

	// Clean preflight.
	rt := e.runtime(false)
	if len(rt.Issues) != 0 {
		t.Fatalf("unexpected issues %+v", rt.Issues)
	}

	// Edit cited lines: stale.
	e.source("greet.go", "package greet\n\n// Greet prints hi.\nfunc Greet() { println(\"hi\") }\n")
	rt = e.runtime(false)
	if len(rt.Issues) != 1 || rt.Issues[0].Kind != Stale || rt.Issues[0].ClaimID != id {
		t.Fatalf("issues %+v", rt.Issues)
	}
	inspected, _ := rt.Session.Inspect(page)
	if inspected[0].Issue == nil || inspected[0].Issue.Kind != Stale {
		t.Fatalf("inspected %+v", inspected)
	}
	// Omitting the stale claim is refused.
	err := Reconcile(rt.Session, page, Proposal{Claims: []ProposedClaim{{Statement: "Another fact.", Evidence: []string{"repo://greet.go#L1"}}}})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("want stale rejection, got %v", err)
	}
	// Updating it clears the debt.
	if err := Reconcile(rt.Session, page, Proposal{Claims: []ProposedClaim{{ID: id, Statement: "Greet prints hi.", Evidence: []string{"repo://greet.go#L3-L4"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Finalize("2026-10-02T00:00:00.000Z", nil); err != nil {
		t.Fatal(err)
	}
	if err := rt.AssertPageDurable(page); err != nil {
		t.Fatal(err)
	}

	// Delete the source: unresolved.
	if err := os.Remove(filepath.Join(e.repo, "greet.go")); err != nil {
		t.Fatal(err)
	}
	rt = e.runtime(false)
	if len(rt.Issues) != 1 || rt.Issues[0].Kind != Unresolved {
		t.Fatalf("issues %+v", rt.Issues)
	}
}

func TestReconcileRules(t *testing.T) {
	e := newEnv(t)
	e.source("a.go", "one\ntwo\nthree\n")
	e.page(page, pageContent)
	s := e.session(nil, nil, nil)
	if err := Reconcile(s, page, Proposal{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty factual page should be refused: %v", err)
	}
	if err := Reconcile(s, page, Proposal{Claims: []ProposedClaim{
		{Statement: "One.", Evidence: []string{"repo://a.go#L1"}},
		{Statement: "Two.", Evidence: []string{"repo://a.go#L2"}},
	}}); err != nil {
		t.Fatal(err)
	}
	claims, _ := s.Inspect(page)
	one, two := claims[0].ID, claims[1].ID

	bad := []Proposal{
		{Claims: []ProposedClaim{{Statement: "X.", Evidence: []string{"repo://a.go"}}, {Statement: " X. ", Evidence: []string{"repo://a.go"}}}},
		{ConfirmedClaimIDs: []string{one}, RetractedClaimIDs: []string{one}},
		{ConfirmedClaimIDs: []string{"claim_not_here"}},
		{Claims: []ProposedClaim{{ID: "claim_not_here", Statement: "X.", Evidence: []string{"repo://a.go"}}}},
		{Claims: []ProposedClaim{{Statement: "No evidence.", Evidence: []string{" "}}}},
		{RetractedClaimIDs: []string{one, two}},
	}
	for i, p := range bad {
		if err := Reconcile(s, page, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: want ErrInvalid, got %v", i, err)
		}
	}

	// An unchanged proposal without id confirms; a retraction of an unknown,
	// unowned id is tolerated; omitted claims are kept.
	if err := Reconcile(s, page, Proposal{
		Claims:            []ProposedClaim{{Statement: "One.", Evidence: []string{"repo://a.go#L1-L1"}}},
		RetractedClaimIDs: []string{"claim_long_gone"},
	}); err != nil {
		t.Fatal(err)
	}
	claims, _ = s.Inspect(page)
	if len(claims) != 2 || claims[0].ID != one || claims[1].ID != two {
		t.Fatalf("claims changed: %+v", claims)
	}

	// Revising with an id updates only what changed.
	if err := Reconcile(s, page, Proposal{Claims: []ProposedClaim{{ID: two, Statement: "Two lines.", Evidence: []string{"repo://a.go#L2"}}}}); err != nil {
		t.Fatal(err)
	}
	claims, _ = s.Inspect(page)
	if claims[1].ID != two || claims[1].Statement != "Two lines." {
		t.Fatalf("update: %+v", claims)
	}

	// A claim owned by another page cannot be retracted from this one.
	other := "/openwiki/other.md"
	e.page(other, pageContent)
	if err := Reconcile(s, other, Proposal{Claims: []ProposedClaim{{Statement: "Three.", Evidence: []string{"repo://a.go#L3"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := Reconcile(s, page, Proposal{RetractedClaimIDs: []string{mustFirstID(t, s, other)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-page retraction: %v", err)
	}
}

func mustFirstID(t *testing.T, s *Session, p string) string {
	t.Helper()
	c, err := s.Inspect(p)
	if err != nil || len(c) == 0 {
		t.Fatalf("inspect %s: %v", p, err)
	}
	return c[0].ID
}

func TestFinalizeRefusesEvidenceDebt(t *testing.T) {
	e := newEnv(t)
	e.source("a.go", "one\ntwo\n")
	e.page(page, pageContent)
	rt := e.runtime(true)
	if err := Reconcile(rt.Session, page, Proposal{Claims: []ProposedClaim{
		{Statement: "One.", Evidence: []string{"repo://a.go#L1"}},
		{Statement: "Two.", Evidence: []string{"repo://a.go#L2"}},
	}}); err != nil {
		t.Fatal(err)
	}
	// Evidence changes between acceptance and finalization.
	e.source("a.go", "ONE\ntwo\n")
	err := rt.Finalize("2026-10-01T00:00:00.000Z", nil)
	if !errors.Is(err, ErrPersistence) || !strings.Contains(err.Error(), "evidence changed") {
		t.Fatalf("want durability failure, got %v", err)
	}
	if pc, _ := e.st.Load(page); pc != nil {
		t.Error("nothing should be persisted")
	}
}

func TestFinalizeCleansUpAndHonorsExclusions(t *testing.T) {
	e := newEnv(t)
	rt, _ := grounded(t, e)

	// A sidecar whose page vanished is an orphan.
	if err := os.Remove(filepath.Join(e.layout.WikiRoot, "concepts", "greet.md")); err != nil {
		t.Fatal(err)
	}
	rt = e.runtime(false)
	if err := rt.Finalize("2026-10-01T00:00:00.000Z", map[string]bool{page: true}); err != nil {
		t.Fatal(err)
	}
	if pc, _ := e.st.Load(page); pc == nil {
		t.Fatal("excluded orphan must be left alone")
	}
	if err := rt.Finalize("2026-10-01T00:00:00.000Z", nil); err != nil {
		t.Fatal(err)
	}
	if pc, _ := e.st.Load(page); pc != nil {
		t.Fatal("orphan sidecar should be removed")
	}
	if err := rt.AssertWikiDurable(nil); err != nil {
		t.Fatal(err)
	}

	// Recorded deletions remove sidecars too.
	rt2, _ := grounded(t, newEnvShared(t, e))
	if err := rt2.Session.RecordDeletion(page); err != nil {
		t.Fatal(err)
	}
	if err := rt2.Finalize("2026-10-01T00:00:00.000Z", nil); err != nil {
		t.Fatal(err)
	}
	if pc, _ := e.st.Load(page); pc != nil {
		t.Fatal("deleted page's sidecar should be removed")
	}
}

// newEnvShared returns e itself; it exists to make the reuse explicit.
func newEnvShared(_ *testing.T, e *env) *env { return e }

func TestSyncSources(t *testing.T) {
	e := newEnv(t)
	e.page(page, "---\ntype: concept\nsources:\n  - id: mine\n    resource: repo://b.go\n    note: hand-written\n  - id: openwiki-source-stale\n    resource: repo://old.go\n---\n\n# P\n")
	err := SyncSources(e.st, map[string][]string{
		page:             {"repo://a.go#L1-L2", "repo://a.go#L5-L9", "repo://b.go#L3-L3", "repo://c.go"},
		"/openwiki/x.md": {"repo://z.go"}, // not present: ignored
	})
	if err != nil {
		t.Fatal(err)
	}
	content, _ := e.st.ReadMarkdown(page)
	want := "sources:\n  - id: mine\n    resource: repo://b.go\n    note: hand-written\n  - id: " + SourceID("repo://a.go") + "\n    resource: repo://a.go\n  - id: " + SourceID("repo://c.go") + "\n    resource: repo://c.go\n"
	if !strings.Contains(content, want) {
		t.Fatalf("got\n%s\nwant block\n%s", content, want)
	}
	if okf.Validate(content) != nil {
		t.Error("invalid after sync")
	}
	// Idempotent.
	if err := SyncSources(e.st, map[string][]string{page: {"repo://a.go#L1-L2", "repo://b.go#L3-L3", "repo://c.go"}}); err != nil {
		t.Fatal(err)
	}
	again, _ := e.st.ReadMarkdown(page)
	if again != content {
		t.Errorf("second sync changed the page:\n%s", again)
	}
	// Sources sort case-insensitively, like upstream's locale collation.
	e.page("/openwiki/order.md", "---\ntype: t\n---\n\n# O\n")
	if err := SyncSources(e.st, map[string][]string{"/openwiki/order.md": {"repo://Makefile", "repo://internal/a.go", "repo://README.md"}}); err != nil {
		t.Fatal(err)
	}
	ordered, _ := e.st.ReadMarkdown("/openwiki/order.md")
	if i, j, k := strings.Index(ordered, "internal/a.go"), strings.Index(ordered, "Makefile"), strings.Index(ordered, "README.md"); !(i < j && j < k) {
		t.Errorf("sources order:\n%s", ordered)
	}
	// Upstream id derivation (sha256 of the resource, 24 hex digits).
	if SourceID("repo://src/agent/wiki-finalizer.ts") != "openwiki-source-adcadc660c1888613ec50f9a" {
		t.Errorf("SourceID mismatch: %s", SourceID("repo://src/agent/wiki-finalizer.ts"))
	}
}

func TestSyncVerification(t *testing.T) {
	e := newEnv(t)
	e.page(page, "---\ntype: concept\nverified: {by: human/alice, at: \"2026-01-01T00:00:00Z\"}\n---\n\n# P\n")
	e.page("/openwiki/b.md", "---\ntype: concept\nverified:\n  - by: openwiki/0.6.1\n    at: 2026-09-30T08:10:27.967Z\n  - by: human/bob\n---\n\n# B\n")
	originals, err := SyncVerification(e.st, map[string]*Verification{
		page:             {By: producer, At: "2026-10-01T00:00:00.000Z"},
		"/openwiki/b.md": nil,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.st.ReadMarkdown(page)
	b, _ := e.st.ReadMarkdown("/openwiki/b.md")
	wantA := "verified:\n  - by: human/alice\n    at: \"2026-01-01T00:00:00Z\"\n  - by: owcli/test\n    at: \"2026-10-01T00:00:00.000Z\"\n"
	if !strings.Contains(a, wantA) {
		t.Errorf("a:\n%s", a)
	}
	if strings.Contains(b, "openwiki/0.6.1") || !strings.Contains(b, "human/bob") {
		t.Errorf("b:\n%s", b)
	}
	if len(originals) != 2 {
		t.Fatalf("originals %v", originals)
	}
	if err := RollbackVerification(e.st, originals, []string{page}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.ReadMarkdown(page); got != originals[page] {
		t.Error("rollback failed")
	}
}

func TestStoreRefusesSymlinks(t *testing.T) {
	e := newEnv(t)
	e.page(page, pageContent)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(e.layout.WikiRoot, ".claims")); err != nil {
		t.Fatal(err)
	}
	pc := PageClaims{SchemaVersion: SchemaVersion, PageVersion: HashContent(pageContent), Claims: []Claim{}}
	if err := e.st.Write(page, pc); !errors.Is(err, ErrSecurity) {
		t.Fatalf("want ErrSecurity, got %v", err)
	}
	if _, err := e.st.Load(page); !errors.Is(err, ErrSecurity) {
		t.Fatalf("want ErrSecurity on load, got %v", err)
	}
}

func TestStoreValidatesSidecars(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.layout.WikiRoot, ".claims", "concepts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hash := HashContent("x")
	ev := `[{"resource":"repo://a.go","version":"v"}]`
	for _, content := range []string{
		`{"schemaVersion":2,"pageVersion":"` + hash + `","claims":[]}`,
		`{"schemaVersion":1,"pageVersion":"md5:x","claims":[]}`,
		`{"schemaVersion":1,"pageVersion":"` + hash + `","claims":[],"extra":1}`,
		`{"schemaVersion":1,"pageVersion":"` + hash + `","claims":[{"id":"c","statement":" s","evidence":` + ev + `}]}`,
		`{"schemaVersion":1,"pageVersion":"` + hash + `","claims":[{"id":"c","statement":"s","evidence":[]}]}`,
		`{"schemaVersion":1,"pageVersion":"` + hash + `","claims":[{"id":"c","statement":"s","evidence":` + ev + `},{"id":"c","statement":"t","evidence":` + ev + `}]}`,
		`not json`,
	} {
		if err := os.WriteFile(filepath.Join(dir, "greet.json"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.Load(page); !errors.Is(err, ErrPersistence) {
			t.Errorf("sidecar %s: want ErrPersistence, got %v", content, err)
		}
	}
}

func TestPrepareRequiresProducer(t *testing.T) {
	e := newEnv(t)
	if _, err := Prepare(e.st, e.resolver, " ", true); err == nil {
		t.Fatal("empty producer must be refused")
	}
}
