package generate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"owcli/internal/llm"
	"owcli/internal/llm/llmtest"
	"owcli/internal/run"
	"owcli/internal/store"
)

func repo(t *testing.T) store.Layout {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "greet.go"), []byte("package greet\n\n// Greet prints hello.\nfunc Greet() { println(\"hello\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	root, _ := filepath.EvalSymlinks(dir)
	return store.Layout{Kind: store.External, RepoRoot: root, WikiRoot: filepath.Join(base, "data", "openwiki")}
}

type turn = func(llm.Request) (llm.Response, error)

const greetPage = "---\ntype: Concept\ntitle: Greeting\ndescription: How greeting works.\n---\n\n# Greeting\n\n`Greet` prints hello.\n"
const quickPage = "---\ntype: Guide\ntitle: Quickstart\ndescription: Start here.\n---\n\n# Quickstart\n\nSee [Greeting](concepts/greet.md).\n"

func claim(statement, evidence string) map[string]any {
	return map[string]any{"claims": []map[string]any{{"statement": statement, "evidence": []string{evidence}}}}
}

func TestGenerateInit(t *testing.T) {
	l := repo(t)
	p := &llmtest.Scripted{Turns: []turn{
		// planner
		llmtest.Call("p1", "ls", map[string]string{}),
		llmtest.Call("p2", "submit_plan", map[string]any{"pages": []map[string]any{
			{"path": "quickstart.md", "title": "Quickstart", "purpose": "Orient newcomers."},
			{"path": "concepts/greet.md", "title": "Greeting", "purpose": "Explain Greet.", "seedPaths": []string{"greet.go"}},
			{"path": "concepts/lost.md", "title": "Lost", "purpose": "A page the worker gives up on."},
		}}),
		// concepts/greet.md: a rejected submission is corrected
		llmtest.Call("g1", "write_file", map[string]string{"path": "openwiki/concepts/greet.md", "content": greetPage}),
		llmtest.Call("g2", "submit_page", claim("Greet prints hello.", "repo://missing.go")),
		func(req llm.Request) (llm.Response, error) {
			res := llmtest.LastResults(req)
			if len(res) != 1 || !res[0].IsError || !strings.Contains(res[0].Content, "does not resolve") {
				t.Errorf("expected the evidence error to reach the model: %+v", res)
			}
			return llmtest.Call("g3", "submit_page", claim("Greet prints hello.", "repo://greet.go#L3-L4"))(req)
		},
		// concepts/lost.md: the worker stops without submitting
		llmtest.Call("l1", "write_file", map[string]string{"path": "openwiki/concepts/lost.md", "content": "partial"}),
		llmtest.Text("I could not finish."),
		func(req llm.Request) (llm.Response, error) {
			last := req.Messages[len(req.Messages)-1].Text
			if !strings.Contains(last, "call write_file with path openwiki/concepts/lost.md") {
				t.Errorf("expected a nudge, got %q", last)
			}
			return llmtest.Text("Still cannot.")(req)
		},
		llmtest.Text("Giving up."),
		// quickstart.md
		llmtest.Call("q1", "write_file", map[string]string{"path": "openwiki/quickstart.md", "content": quickPage}),
		llmtest.Call("q2", "submit_page", claim("The Greet function lives in greet.go.", "repo://greet.go")),
	}}
	var stages []string
	res, err := Generate(context.Background(), Options{
		Env:      run.Env{Layout: l, Producer: "owcli/test", Model: "scripted", Now: func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }},
		Provider: p, Mode: run.Init, Message: "focus on greeting",
		Progress: func(pr Progress) {
			if pr.Event == nil {
				stages = append(stages, pr.Stage+":"+pr.Page)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Written, ",") != "openwiki/concepts/greet.md,openwiki/quickstart.md" || strings.Join(res.Skipped, ",") != "openwiki/concepts/lost.md" {
		t.Fatalf("result %+v", res)
	}
	if res.Finish.Status != store.StatusInterrupted {
		t.Errorf("a skipped page leaves the run interrupted: %+v", res.Finish)
	}
	if _, err := os.Stat(filepath.Join(l.WikiRoot, "concepts", "lost.md")); !os.IsNotExist(err) {
		t.Error("the abandoned page must be rolled back")
	}
	if want := "begin:,page:openwiki/concepts/greet.md,page:openwiki/concepts/lost.md,skip:openwiki/concepts/lost.md,page:openwiki/quickstart.md,finish:"; strings.Join(stages, ",") != want {
		t.Errorf("stages %v", stages)
	}

	// Prompts carry the context each agent needs.
	planner := p.Requests[0]
	if planner.System != plannerSystem || !strings.Contains(planner.Messages[0].Text, "focus on greeting") || !strings.Contains(planner.Messages[0].Text, "practical engineering wiki") {
		t.Errorf("planner prompt:\n%s", planner.Messages[0].Text)
	}
	worker := p.Requests[2]
	w := worker.Messages[0].Text
	if worker.System != workerSystem || !strings.Contains(w, "Write the new wiki page openwiki/concepts/greet.md") || !strings.Contains(w, "Start your research at: greet.go") || !strings.Contains(w, "- quickstart.md: Quickstart — Orient newcomers.") {
		t.Errorf("worker prompt:\n%s", w)
	}
	var names []string
	for _, d := range worker.Tools {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "ls,glob,grep,read_file,git_log,write_file,edit_file,inspect_page_claims,submit_page" {
		t.Errorf("worker tools %v", names)
	}
	quick := p.Requests[len(p.Requests)-2].Messages[0].Text // quickstart worker's first request
	if !strings.Contains(quick, "This is the quickstart") {
		t.Errorf("quickstart prompt:\n%s", quick)
	}
}

func TestGenerateAbortsOnProviderFailure(t *testing.T) {
	l := repo(t)
	boom := errors.New("401 unauthorized")
	p := &llmtest.Scripted{Turns: []turn{
		llmtest.Call("p1", "submit_plan", map[string]any{"pages": []map[string]any{{"path": "quickstart.md", "title": "Q", "purpose": "Q."}}}),
		llmtest.Call("q1", "write_file", map[string]string{"path": "openwiki/quickstart.md", "content": quickPage}),
		func(llm.Request) (llm.Response, error) { return llm.Response{}, boom },
	}}
	env := run.Env{Layout: l, Producer: "owcli/test", Model: "scripted"}
	_, err := Generate(context.Background(), Options{Env: env, Provider: p, Mode: run.Init})
	if !errors.Is(err, boom) {
		t.Fatalf("want provider error, got %v", err)
	}
	if _, err := os.Stat(l.RunPath()); err != nil {
		t.Fatal("the run must stay resumable")
	}
	if _, err := os.Stat(filepath.Join(l.WikiRoot, "quickstart.md")); !os.IsNotExist(err) {
		t.Error("partial page must be rolled back")
	}

	// Resuming skips planning and retries the page.
	p2 := &llmtest.Scripted{Turns: []turn{
		llmtest.Call("q1", "write_file", map[string]string{"path": "openwiki/quickstart.md", "content": quickPage}),
		llmtest.Call("q2", "submit_page", claim("Greet is in greet.go.", "repo://greet.go")),
	}}
	res, err := Generate(context.Background(), Options{Env: env, Provider: p2, Mode: run.Init})
	if err != nil || !res.Begin.Resumed || res.Finish.Status != store.StatusComplete {
		t.Fatalf("resume: %+v %v", res, err)
	}
	if p2.Requests[0].System != workerSystem {
		t.Error("a resumed run with a plan goes straight to the pages")
	}
}

func TestGenerateNoop(t *testing.T) {
	l := repo(t)
	env := run.Env{Layout: l, Producer: "owcli/test", Model: "scripted"}
	p := &llmtest.Scripted{Turns: []turn{
		llmtest.Call("p1", "submit_plan", map[string]any{"pages": []map[string]any{{"path": "quickstart.md", "title": "Q", "purpose": "Q."}}}),
		llmtest.Call("q1", "write_file", map[string]string{"path": "openwiki/quickstart.md", "content": quickPage}),
		llmtest.Call("q2", "submit_page", claim("Greet is in greet.go.", "repo://greet.go")),
	}}
	if _, err := Generate(context.Background(), Options{Env: env, Provider: p, Mode: run.Init}); err != nil {
		t.Fatal(err)
	}
	res, err := Generate(context.Background(), Options{Env: env, Provider: &llmtest.Scripted{}, Mode: run.Update})
	if err != nil || !res.Noop {
		t.Fatalf("update of a fresh wiki should be a noop: %+v %v", res, err)
	}
}
