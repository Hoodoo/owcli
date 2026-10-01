package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owcli/internal/llm"
	"owcli/internal/llm/llmtest"
	"owcli/internal/store"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, c := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// workspace builds an external-layout workspace whose repo also carries a
// stale upstream openwiki/ directory that must stay invisible.
func workspace(t *testing.T) *Workspace {
	t.Helper()
	base := t.TempDir()
	repo, wiki := filepath.Join(base, "repo"), filepath.Join(base, "data", "openwiki")
	write(t, repo, map[string]string{
		"main.go":              "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
		"src/lib/util.go":      "package lib\n\n// Helper does things.\nfunc Helper() {}\n",
		"src/lib/util_test.go": "package lib\n",
		"secret/key.pem":       "KEY\n",
		"docs/notes.md":        "# Notes\nhelper usage\n",
		"bin/blob":             "a\x00b",
		"openwiki/stale.md":    "# Stale upstream page\n",
		".openwikiignore":      "secret/\n",
	})
	write(t, wiki, map[string]string{
		"quickstart.md":           "# Quickstart\n",
		"concepts/helper.md":      "# Helper\n",
		".claims/quickstart.json": "{}",
		".run.json":               "{}",
	})
	l := store.Layout{Kind: store.External, RepoRoot: repo, WikiRoot: wiki}
	w, err := NewWorkspace(l)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWorkspaceReads(t *testing.T) {
	w := workspace(t)
	if s, err := w.ReadFile("./src//lib/util.go"); err != nil || !strings.Contains(s, "Helper") {
		t.Fatalf("read: %q %v", s, err)
	}
	if s, err := w.ReadFile("/openwiki/quickstart.md"); err != nil || s != "# Quickstart\n" {
		t.Fatalf("wiki read: %q %v", s, err)
	}
	denied := []string{"secret/key.pem", "../outside", "src/lib/../lib/util.go", "openwiki/.claims/quickstart.json", "openwiki/.run.json", ".git/config/../../../x"}
	for _, p := range denied {
		if _, err := w.ReadFile(p); !errors.Is(err, ErrDenied) {
			t.Errorf("ReadFile(%q) should be denied, got %v", p, err)
		}
	}
	if _, err := w.ReadFile("bin/blob"); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Errorf("binary: %v", err)
	}
	if _, err := w.ReadFile("missing.go"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing: %v", err)
	}

	// A symlink pointing outside the repository is refused.
	outside := filepath.Join(t.TempDir(), "x.txt")
	write(t, filepath.Dir(outside), map[string]string{"x.txt": "outside"})
	if err := os.Symlink(outside, filepath.Join(w.Repo, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ReadFile("link.txt"); !errors.Is(err, ErrDenied) {
		t.Errorf("symlink escape: %v", err)
	}
}

func TestWorkspaceListAndWalk(t *testing.T) {
	w := workspace(t)
	entries, err := w.List("")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Path)
	}
	got := strings.Join(names, ",")
	if got != ".openwikiignore,bin,docs,main.go,openwiki,src" {
		t.Errorf("root listing %s", got)
	}
	wikiEntries, _ := w.List("openwiki")
	var wikiNames []string
	for _, e := range wikiEntries {
		wikiNames = append(wikiNames, e.Path)
	}
	if strings.Join(wikiNames, ",") != "openwiki/concepts,openwiki/quickstart.md" {
		t.Errorf("wiki listing %v", wikiNames)
	}

	var walked []string
	if err := w.Walk("", func(e Entry) error { walked = append(walked, e.Path); return nil }); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(walked, ",")
	for _, want := range []string{"main.go", "src/lib/util.go", "openwiki/quickstart.md", "openwiki/concepts/helper.md"} {
		if !strings.Contains(all, want) {
			t.Errorf("walk missing %s: %s", want, all)
		}
	}
	for _, bad := range []string{"secret", "stale.md", ".claims", ".run.json"} {
		if strings.Contains(all, bad) {
			t.Errorf("walk leaked %s: %s", bad, all)
		}
	}
}

func TestWritesConfinedToAssignedPage(t *testing.T) {
	w := workspace(t)
	w.Writable["concepts/helper.md"] = true
	if err := w.WriteFile("openwiki/concepts/helper.md", "# Helper\n\nNew.\n"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"openwiki/quickstart.md", "main.go", "openwiki/.claims/x.json", "openwiki/concepts/../quickstart.md"} {
		if err := w.WriteFile(p, "x"); !errors.Is(err, ErrDenied) {
			t.Errorf("WriteFile(%q) should be denied, got %v", p, err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(w.Repo, "main.go")); !strings.Contains(string(data), "println") {
		t.Error("repository file changed")
	}
}

func call(t *testing.T, tools []Tool, name string, input any) (string, error) {
	t.Helper()
	for _, tool := range tools {
		if tool.Def().Name == name {
			res, err := tool.Call(context.Background(), llmtest.Input(input))
			return res.Text, err
		}
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

func TestReadTools(t *testing.T) {
	w := workspace(t)
	tools := ReadTools(w)
	if out, _ := call(t, tools, "glob", map[string]string{"pattern": "**/*_test.go"}); out != "src/lib/util_test.go" {
		t.Errorf("glob: %q", out)
	}
	if out, _ := call(t, tools, "glob", map[string]string{"pattern": "*.go", "path": "src/lib"}); out != "src/lib/util.go\nsrc/lib/util_test.go" {
		t.Errorf("glob in dir: %q", out)
	}
	if out, _ := call(t, tools, "glob", map[string]string{"pattern": "openwiki/**/*.md"}); out != "openwiki/concepts/helper.md\nopenwiki/quickstart.md" {
		t.Errorf("glob wiki: %q", out)
	}
	if out, _ := call(t, tools, "grep", map[string]any{"pattern": "helper", "ignore_case": true, "glob": "*.go"}); out != "src/lib/util.go:3: // Helper does things.\nsrc/lib/util.go:4: func Helper() {}" {
		t.Errorf("grep: %q", out)
	}
	if out, _ := call(t, tools, "grep", map[string]any{"pattern": "KEY"}); out != "no matches" {
		t.Errorf("grep must skip ignored files: %q", out)
	}
	if out, _ := call(t, tools, "grep", map[string]any{"pattern": "println", "path": "main.go"}); out != "main.go:4: \tprintln(\"hi\")" {
		t.Errorf("grep file: %q", out)
	}
	if out, _ := call(t, tools, "read_file", map[string]any{"path": "main.go", "offset": 3, "limit": 2}); out != "     3\tfunc main() {\n     4\t\tprintln(\"hi\")\n... 1 more lines; continue with offset 5\n" {
		t.Errorf("read_file: %q", out)
	}
	if _, err := call(t, tools, "read_file", map[string]any{"path": "secret/key.pem"}); !errors.Is(err, ErrDenied) {
		t.Errorf("read_file ignored: %v", err)
	}
}

func TestGitLog(t *testing.T) {
	w := workspace(t)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "first commit"}} {
		if out, err := exec.Command("git", append([]string{"-C", w.Repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	out, err := call(t, ReadTools(w), "git_log", map[string]any{"path": "main.go"})
	if err != nil || !strings.Contains(out, "first commit") {
		t.Fatalf("git_log: %q %v", out, err)
	}
	if _, err := call(t, ReadTools(w), "git_log", map[string]any{"path": "secret/key.pem"}); !errors.Is(err, ErrDenied) {
		t.Errorf("git_log on ignored path: %v", err)
	}
}

func TestEditFile(t *testing.T) {
	w := workspace(t)
	w.Writable["concepts/helper.md"] = true
	tools := WriteTools(w)
	if _, err := call(t, tools, "write_file", map[string]string{"path": "openwiki/concepts/helper.md", "content": "a b a\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, tools, "edit_file", map[string]any{"path": "openwiki/concepts/helper.md", "old_string": "a", "new_string": "c"}); err == nil {
		t.Error("ambiguous edit should fail")
	}
	if _, err := call(t, tools, "edit_file", map[string]any{"path": "openwiki/concepts/helper.md", "old_string": "a", "new_string": "c", "replace_all": true}); err != nil {
		t.Fatal(err)
	}
	if s, _ := w.ReadFile("openwiki/concepts/helper.md"); s != "c b c\n" {
		t.Errorf("content %q", s)
	}
	if _, err := call(t, tools, "edit_file", map[string]any{"path": "openwiki/concepts/helper.md", "old_string": "zzz", "new_string": "c"}); err == nil {
		t.Error("missing old_string should fail")
	}
}

func TestRunLoop(t *testing.T) {
	w := workspace(t)
	w.Writable["concepts/helper.md"] = true
	done := NewTool("finish", "Finish.", `{"type":"object"}`, func(context.Context, struct{}) (Result, error) {
		return Result{Text: "finished", Stop: true}, nil
	})
	p := &llmtest.Scripted{Turns: []func(llm.Request) (llm.Response, error){
		llmtest.Calls(
			llm.ToolCall{ID: "1", Name: "read_file", Input: llmtest.Input(map[string]string{"path": "src/lib/util.go"})},
			llm.ToolCall{ID: "2", Name: "read_file", Input: llmtest.Input(map[string]string{"path": "secret/key.pem"})},
			llm.ToolCall{ID: "3", Name: "nope", Input: llmtest.Input(map[string]string{})},
		),
		func(llm.Request) (llm.Response, error) {
			return llm.Response{Message: llm.Message{Role: llm.Assistant, Text: "partial"}, Stop: llm.StopMaxTokens}, nil
		},
		llmtest.Call("4", "write_file", map[string]string{"path": "openwiki/concepts/helper.md", "content": "# Helper\n"}),
		llmtest.Call("5", "finish", map[string]string{}),
	}}
	var events []string
	out, err := Run(context.Background(), p, Options{
		System: "sys", Prompt: "document helper", Tools: append(append(ReadTools(w), WriteTools(w)...), done),
		OnEvent: func(e Event) {
			if e.Kind != "usage" {
				events = append(events, e.Kind+":"+e.Tool)
			}
		},
	})
	if err != nil || !out.Stopped || out.Steps != 4 {
		t.Fatalf("outcome %+v %v", out, err)
	}
	results := llmtest.LastResults(p.Requests[1])
	if len(results) != 3 || results[0].IsError || !results[1].IsError || !strings.Contains(results[2].Content, "unknown tool") {
		t.Fatalf("results %+v", results)
	}
	if last := p.Requests[2].Messages[len(p.Requests[2].Messages)-1]; last.Text != maxTokensNudge {
		t.Errorf("max_tokens should be followed by a nudge: %+v", last)
	}
	if p.Requests[0].System != "sys" || len(p.Requests[0].Tools) != 8 {
		t.Errorf("request %+v", p.Requests[0])
	}
	if strings.Join(events, ",") != "tool:read_file,tool_error:read_file,tool_error:nope,text:,tool:write_file,tool:finish" {
		t.Errorf("events %v", events)
	}
}

func TestRunLimitsAndRefusal(t *testing.T) {
	loop := llmtest.Call("1", "ls", map[string]string{})
	p := &llmtest.Scripted{Turns: []func(llm.Request) (llm.Response, error){loop, loop}}
	if _, err := Run(context.Background(), p, Options{Prompt: "x", Tools: ReadTools(workspace(t)), MaxSteps: 2}); !errors.Is(err, ErrStepLimit) {
		t.Errorf("step limit: %v", err)
	}
	refuse := &llmtest.Scripted{Turns: []func(llm.Request) (llm.Response, error){func(llm.Request) (llm.Response, error) {
		return llm.Response{Stop: llm.StopRefusal, Detail: "cyber"}, nil
	}}}
	if _, err := Run(context.Background(), refuse, Options{Prompt: "x"}); !errors.Is(err, ErrRefused) {
		t.Errorf("refusal: %v", err)
	}
	end := &llmtest.Scripted{Turns: []func(llm.Request) (llm.Response, error){llmtest.Text("all done")}}
	if out, err := Run(context.Background(), end, Options{Prompt: "x"}); err != nil || out.Text != "all done" || out.Stopped {
		t.Errorf("end turn: %+v %v", out, err)
	}
}

func TestGlobRegexp(t *testing.T) {
	cases := map[string][2][]string{
		"**/*.go":        {{"a.go", "x/y/z.go"}, {"a.go.txt"}},
		"src/*.{ts,tsx}": {{"src/a.ts", "src/b.tsx"}, {"src/x/a.ts", "src/a.js"}},
		"file?.md":       {{"file1.md"}, {"file10.md"}},
	}
	for g, c := range cases {
		re, err := globRegexp(g)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range c[0] {
			if !re.MatchString(s) {
				t.Errorf("%s should match %s", g, s)
			}
		}
		for _, s := range c[1] {
			if re.MatchString(s) {
				t.Errorf("%s should not match %s", g, s)
			}
		}
	}
}
