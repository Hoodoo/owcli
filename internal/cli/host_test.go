package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattn/go-isatty"
)

// step runs one `owcli run` command as an agent would and decodes its JSON.
func step(t *testing.T, input string, args ...string) (map[string]any, bool) {
	t.Helper()
	out, err := runCLIIn(input, append([]string{"run"}, args...)...)
	var v map[string]any
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
		t.Fatalf("run %v: output is not JSON (%v): %q", args, jerr, out)
	}
	return v, err == nil
}

func mustStep(t *testing.T, input string, args ...string) map[string]any {
	t.Helper()
	v, ok := step(t, input, args...)
	if !ok {
		t.Fatalf("run %v failed: %v", args, v["error"])
	}
	return v
}

func wikiDir(t *testing.T) string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(os.Getenv("XDG_DATA_HOME"), "owcli", "wikis", "*", "openwiki"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("external wiki not found: %v %v", dirs, err)
	}
	return dirs[0]
}

func writePage(t *testing.T, wiki, path, content string) {
	t.Helper()
	p := filepath.Join(wiki, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const greetDoc = "---\ntype: Concept\ntitle: Greeting\ndescription: How greeting works.\n---\n\n# Greeting\n\n`Greet` prints hello. See the [quickstart](../quickstart.md).\n"

func TestAgentDrivenLifecycle(t *testing.T) {
	repo := cliRepo(t)

	begin := mustStep(t, "", "begin", "init", "--external", "--message", "be brief")
	if begin["status"] != "planning" || begin["layout"] != "external" || begin["message"] != "be brief" || !strings.Contains(begin["next"].(string), "owcli run plan") {
		t.Fatalf("begin: %v", begin)
	}
	plan := mustStep(t, `{"pages": [
		{"path": "quickstart.md", "title": "Quickstart", "purpose": "Orient."},
		{"path": "concepts/greet.md", "title": "Greeting", "purpose": "Explain Greet.", "seedPaths": ["greet.go"], "relatedPages": ["quickstart.md"]}
	], "instructions": "Keep pages short."}`, "plan")
	if plan["status"] != "accepted" {
		t.Fatalf("plan: %v", plan)
	}
	if v, ok := step(t, `{"pages": [], "bogus": 1}`, "plan"); ok || v["error"].(map[string]any)["code"] != "invalid_input" {
		t.Fatalf("unknown plan fields must be rejected: %v", v)
	}

	next := mustStep(t, "", "next")
	job := next["job"].(map[string]any)
	if job["path"] != "openwiki/concepts/greet.md" || next["instructions"] != "Keep pages short." || job["relatedPages"].([]any)[0] != "openwiki/quickstart.md" {
		t.Fatalf("next: %v", next)
	}
	wiki := wikiDir(t)
	writePage(t, wiki, "concepts/greet.md", greetDoc)
	bad, ok := step(t, `{"claims": [{"statement": "Greet prints hello.", "evidence": ["repo://nope.go"]}]}`, "submit", job["id"].(string))
	if ok || bad["error"].(map[string]any)["code"] != "invalid_input" || !strings.Contains(bad["error"].(map[string]any)["message"].(string), "does not resolve") {
		t.Fatalf("bad evidence must be invalid_input: %v", bad)
	}
	sub := mustStep(t, `{"claims": [{"statement": "Greet prints hello.", "evidence": [{"resource": "repo://greet.go#L3-L4"}]}]}`, "submit", job["id"].(string))
	if sub["remaining"] != float64(1) {
		t.Fatalf("submit: %v", sub)
	}

	// The agent abandons the quickstart; skip restores the page.
	quick := mustStep(t, "", "next")["job"].(map[string]any)
	writePage(t, wiki, "quickstart.md", "half-written")
	if v := mustStep(t, "", "skip", quick["id"].(string)); v["status"] != "skipped" {
		t.Fatalf("skip: %v", v)
	}
	if _, err := os.Stat(filepath.Join(wiki, "quickstart.md")); !os.IsNotExist(err) {
		t.Fatal("skip must restore the absent page")
	}

	// A new session resumes: the skipped job is pending again.
	resumed := mustStep(t, "", "begin", "init")
	if resumed["status"] != "generating" || resumed["resumed"] != true {
		t.Fatalf("resume: %v", resumed)
	}
	again := mustStep(t, "", "next")["job"].(map[string]any)
	if again["id"] != quick["id"] {
		t.Fatalf("expected the skipped job again, got %v", again)
	}
	writePage(t, wiki, "quickstart.md", "---\ntype: Guide\ntitle: Quickstart\ndescription: Start here.\n---\n\n# Quickstart\n\nRead [Greeting](concepts/greet.md).\n")
	mustStep(t, `{"claims": [{"statement": "Greet lives in greet.go.", "evidence": ["repo://greet.go"]}]}`, "submit", again["id"].(string))
	if v := mustStep(t, "", "next"); v["status"] != "complete" {
		t.Fatalf("next after last page: %v", v)
	}
	fin := mustStep(t, "", "finish")
	if fin["status"] != "complete" || fin["wikiCommit"] == nil {
		t.Fatalf("finish: %v", fin)
	}
	home := filepath.Dir(wiki)
	if log := gitRun(t, home, "log", "--format=%s"); !strings.Contains(log, "owcli init complete: 2 page(s) written @ repo") {
		t.Fatalf("wiki history: %q", log)
	}
	if files := gitRun(t, home, "ls-files"); strings.Contains(files, ".run") || !strings.Contains(files, "openwiki/.claims/quickstart.json") {
		t.Fatalf("wiki tracked files: %q", files)
	}
	if _, err := os.Stat(filepath.Join(wiki, ".run-snapshots")); !os.IsNotExist(err) {
		t.Error("snapshots must be cleaned up at finish")
	}
	if status := gitRun(t, repo, "status", "--porcelain", "--ignored"); status != "" {
		t.Fatalf("repository changed:\n%s", status)
	}
	if v := mustStep(t, "", "begin", "update"); v["status"] != "noop" {
		t.Fatalf("expected noop: %v", v)
	}

	// The source changes: check fails, and an update with no planned pages
	// still gets the stale page.
	writeRepoFile(t, repo, "greet.go", "package greet\n\n// Greet prints hi.\nfunc Greet() { println(\"hi\") }\n")
	commitAll(t, repo, "hi")
	if _, err := runCLI("check"); err == nil {
		t.Fatal("check should fail on a stale Claim")
	}
	gitRun(t, repo, "switch", "-q", "-c", "feature")
	onFeature := mustStep(t, "", "begin", "update")
	if onFeature["branch"] != "feature" || onFeature["warning"] == nil {
		t.Fatalf("begin on a work branch should warn: %v", onFeature)
	}
	gitRun(t, repo, "switch", "-q", "-")
	upd := mustStep(t, "", "begin", "update")
	changed, _ := upd["changedPaths"].([]any)
	if upd["status"] != "planning" || len(changed) != 1 || changed[0] != "greet.go" || upd["claimIssues"] == nil {
		t.Fatalf("update begin: %v", upd)
	}
	mustStep(t, `{"pages": []}`, "plan")
	stale := mustStep(t, "", "next")["job"].(map[string]any)
	attention := stale["claimsRequiringAttention"].([]any)
	if stale["path"] != "openwiki/concepts/greet.md" || len(attention) != 1 {
		t.Fatalf("stale job: %v", stale)
	}
	id := attention[0].(map[string]any)["id"].(string)
	if v := mustStep(t, "", "inspect", stale["id"].(string)); len(v["claims"].([]any)) != 1 {
		t.Fatalf("inspect: %v", v)
	}
	writePage(t, wiki, "concepts/greet.md", strings.Replace(greetDoc, "prints hello", "prints hi", 1))
	mustStep(t, `{"claims": [{"id": "`+id+`", "statement": "Greet prints hi.", "evidence": ["repo://greet.go#L3-L4"]}]}`, "submit", stale["id"].(string))
	// The quickstart cites the whole file, so it is stale too; its fact still
	// holds, so the agent rechecks and confirms it.
	qs := mustStep(t, "", "next")["job"].(map[string]any)
	qid := qs["claimsRequiringAttention"].([]any)[0].(map[string]any)["id"].(string)
	if qs["path"] != "openwiki/quickstart.md" {
		t.Fatalf("second stale job: %v", qs)
	}
	mustStep(t, `{"confirmedClaimIds": ["`+qid+`"]}`, "submit", qs["id"].(string))
	if v := mustStep(t, "", "finish"); v["status"] != "complete" {
		t.Fatalf("update finish: %v", v)
	}
	if n := strings.Count(gitRun(t, home, "log", "--format=%s"), "\n"); n != 2 {
		t.Fatalf("expected two wiki commits, got %d", n)
	}
	if !strings.Contains(gitRun(t, home, "log", "-1", "--format=%B"), "Source commit: "+strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))) {
		t.Error("the wiki commit should record the documented source commit")
	}
	if out, err := runCLI("check"); err != nil {
		t.Fatalf("check after update: %s", out)
	}
}

func TestRunWithoutActiveRun(t *testing.T) {
	cliRepo(t)
	if _, err := runCLI("bind", "--external"); err != nil {
		t.Fatal(err)
	}
	v, ok := step(t, "", "next")
	if ok || v["error"].(map[string]any)["code"] != "not_found" {
		t.Fatalf("next without a run: %v", v)
	}
	if v, ok := step(t, "", "begin", "sideways"); ok || v["error"] == nil {
		t.Fatalf("bad mode: %v", v)
	}
}

func TestInstructions(t *testing.T) {
	out, err := runCLI("quickstart")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"owcli run begin update", "owcli run submit <jobId>", "## Where wikis live", "$XDG_CONFIG_HOME/owcli/bindings.json", "owcli bindings", "## Planning standard", "## Page standard", "## Claim standard", "repo://"} {
		if !strings.Contains(out, want) {
			t.Errorf("quickstart lacks %q", want)
		}
	}
	block, err := runCLI("agents-md", "--print")
	if err != nil || !strings.HasPrefix(block, agentsStart) || !strings.Contains(block, "digraph owcli") || len(strings.Split(block, "\n")) > 60 {
		t.Fatalf("compact block (%d lines): %v", len(strings.Split(block, "\n")), err)
	}

	cliRepo(t)
	if _, err := runCLI("bind", "--external"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI("agents-md"); err == nil || !strings.Contains(err.Error(), "--print") {
		t.Fatalf("agents-md on an external binding must point to --print: %v", err)
	}
}

func TestRunInputFromFileAndTerminal(t *testing.T) {
	cliRepo(t)
	mustStep(t, "", "begin", "init", "--external")
	plan := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(plan, []byte(`{"pages": [{"path": "quickstart.md", "title": "Quickstart", "purpose": "Orient.", "seedPaths": ["greet.go"]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := mustStep(t, "", "plan", "--file", plan); v["status"] != "accepted" {
		t.Fatalf("plan --file: %v", v)
	}

	tty, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil || !isatty.IsTerminal(tty.Fd()) {
		t.Skip("no pseudo-terminal available")
	}
	defer tty.Close()
	job := mustStep(t, "", "next")["job"].(map[string]any)
	cmd := NewRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(tty)
	cmd.SetArgs([]string{"run", "submit", job["id"].(string)})
	if err := cmd.Execute(); err == nil || !strings.Contains(out.String(), "invalid_input") || !strings.Contains(out.String(), "--file") {
		t.Fatalf("submit with a terminal on stdin must fail fast: %v %s", err, out.String())
	}
}
