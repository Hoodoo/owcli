package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owcli/internal/config"
	"owcli/internal/llm"
	"owcli/internal/llm/llmtest"
)

type turn = func(llm.Request) (llm.Response, error)

// cliRepo creates a committed repository, isolated XDG dirs, and makes the
// repository the working directory for the test.
func cliRepo(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, repo, "greet.go", "package greet\n\n// Greet prints hello.\nfunc Greet() { println(\"hello\") }\n")
	gitRun(t, repo, "init", "-q")
	commitAll(t, repo, "init")
	wd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	return repo
}

func writeRepoFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func commitAll(t *testing.T, repo, msg string) {
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", msg)
}

// scripted makes the CLI use p instead of a real provider.
func scripted(t *testing.T, p *llmtest.Scripted) {
	t.Helper()
	old := providerFactory
	providerFactory = func(config.Config) (llm.Provider, error) { return p, nil }
	t.Cleanup(func() { providerFactory = old })
}

const quick = "---\ntype: Guide\ntitle: Quickstart\ndescription: Start here.\n---\n\n# Quickstart\n\n`Greet` prints hello.\n"

func initTurns() []turn {
	return []turn{
		llmtest.Call("p", "submit_plan", map[string]any{"pages": []map[string]any{{"path": "quickstart.md", "title": "Quickstart", "purpose": "Orient."}}}),
		llmtest.Call("w", "write_file", map[string]string{"path": "openwiki/quickstart.md", "content": quick}),
		llmtest.Call("s", "submit_page", map[string]any{"claims": []map[string]any{{"statement": "Greet prints hello.", "evidence": []string{"repo://greet.go#L3-L4"}}}}),
	}
}

func TestCLIExternalLifecycle(t *testing.T) {
	repo := cliRepo(t)
	scripted(t, &llmtest.Scripted{Turns: initTurns()})

	out, err := runCLI("init", "--external")
	if err != nil || !strings.Contains(out, "Init complete") || !strings.Contains(out, "1 page(s) written") {
		t.Fatalf("init: %q %v", out, err)
	}
	if status := gitRun(t, repo, "status", "--porcelain", "--ignored"); status != "" {
		t.Fatalf("external init touched the repository:\n%s", status)
	}
	if _, err := runCLI("init", "--agents-md"); err == nil || !strings.Contains(err.Error(), "in-repo") {
		t.Errorf("--agents-md with an external wiki must be refused: %v", err)
	}

	out, err = runCLI("status")
	if err != nil || !strings.Contains(out, "(external)") || !strings.Contains(out, "init complete") || !strings.Contains(out, "1, 1 Claim(s)") || !strings.Contains(out, "all Claims current") {
		t.Fatalf("status: %q %v", out, err)
	}
	if out, err = runCLI("check"); err != nil || !strings.Contains(out, "OK: 1 page(s), 1 Claim(s)") {
		t.Fatalf("check: %q %v", out, err)
	}
	if out, err = runCLI("search", "greet hello"); err != nil || !strings.Contains(out, "openwiki/quickstart.md#quickstart") {
		t.Fatalf("search: %q %v", out, err)
	}
	if out, err = runCLI("update"); err != nil || !strings.Contains(out, "up to date") {
		t.Fatalf("noop update: %q %v", out, err)
	}

	// Change the cited source: check fails, update reconciles.
	writeRepoFile(t, repo, "greet.go", "package greet\n\n// Greet prints hi.\nfunc Greet() { println(\"hi\") }\n")
	commitAll(t, repo, "hi")
	out, err = runCLI("check")
	if !errors.Is(err, errCheckFailed) || !strings.Contains(out, "Claims needing recheck (1)") {
		t.Fatalf("check after change: %q %v", out, err)
	}
	p := &llmtest.Scripted{Turns: []turn{
		llmtest.Call("p", "submit_plan", map[string]any{"pages": []map[string]any{{"path": "quickstart.md", "title": "Quickstart", "purpose": "Orient."}}}),
		func(req llm.Request) (llm.Response, error) {
			// The worker is told which Claim needs attention.
			text := req.Messages[0].Text
			start := strings.Index(text, "claim_")
			if start < 0 {
				t.Fatalf("worker prompt lacks the stale claim:\n%s", text)
			}
			id := text[start : start+len("claim_")+32]
			return llmtest.Calls(
				llm.ToolCall{ID: "w", Name: "write_file", Input: llmtest.Input(map[string]string{"path": "openwiki/quickstart.md", "content": strings.Replace(quick, "hello", "hi", 1)})},
				llm.ToolCall{ID: "s", Name: "submit_page", Input: llmtest.Input(map[string]any{"claims": []map[string]any{{"id": id, "statement": "Greet prints hi.", "evidence": []string{"repo://greet.go#L3-L4"}}}})},
			)(req)
		},
	}}
	scripted(t, p)
	if out, err = runCLI("update"); err != nil || !strings.Contains(out, "Update complete") {
		t.Fatalf("update: %q %v", out, err)
	}
	if out, err = runCLI("check"); err != nil {
		t.Fatalf("check after update: %q %v", out, err)
	}
}

func TestCLIInRepoAgentsMD(t *testing.T) {
	repo := cliRepo(t)
	writeRepoFile(t, repo, "CLAUDE.md", "# Project notes\n")
	scripted(t, &llmtest.Scripted{Turns: initTurns()})
	if out, err := runCLI("init", "--agents-md"); err != nil || !strings.Contains(out, "Init complete") {
		t.Fatalf("init: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "openwiki", "quickstart.md")); err != nil {
		t.Fatal("in-repo wiki expected")
	}
	agents, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	claude, _ := os.ReadFile(filepath.Join(repo, "CLAUDE.md"))
	if !strings.Contains(string(agents), agentsStart) || !strings.HasPrefix(string(claude), "# Project notes\n\n"+agentsStart) {
		t.Fatalf("AGENTS.md:\n%s\nCLAUDE.md:\n%s", agents, claude)
	}
	// Refreshing is idempotent.
	if got := withBlock(string(claude)); got != string(claude) {
		t.Error("block refresh should be idempotent")
	}
}

func TestCLIUpdateNeedsBinding(t *testing.T) {
	cliRepo(t)
	scripted(t, &llmtest.Scripted{})
	if _, err := runCLI("update"); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("update without a wiki: %v", err)
	}
	if _, err := runCLI("status"); err == nil {
		t.Fatal("status without a binding should fail")
	}
}
