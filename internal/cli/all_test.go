package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hoodoo/owcli/internal/store"
)

// TestStatusAndCheckAll covers a healthy wiki, a bound repository that was
// deleted, and a workspace member without a wiki, from outside any repository.
func TestStatusAndCheckAll(t *testing.T) {
	healthy, err := filepath.EvalSymlinks(cliRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(healthy, "openwiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	newRepo := func(name string) string {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, dir, "init", "-q")
		root, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		return root
	}
	gone, bare := newRepo("gone"), newRepo("bare")
	for _, args := range [][]string{{"bind", healthy}, {"bind", "--external", gone}} {
		if _, err := runCLI(args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	dirs, _ := store.DefaultDirs()
	if _, err := dirs.SaveWorkspaces([]store.WorkspaceDraft{{Name: "Base", Roots: []string{healthy, bare}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI("status", "--all")
	if err != nil || strings.Count(out, "unavailable:") != 2 || !strings.Contains(out, "repository not found") || !strings.Contains(out, "has no wiki") || !strings.Contains(out, "all Claims current") {
		t.Fatalf("status --all: %v\n%s", err, out)
	}

	out, err = runCLI("check", "--all")
	if err != errCheckFailed || !strings.Contains(out, "ok    ") || strings.Count(out, "FAIL  ") != 2 || !strings.Contains(out, "3 wiki(s) checked, 2 with problems") {
		t.Fatalf("check --all: %v\n%s", err, out)
	}

	out, err = runCLI("check", "--all", "--json")
	var v struct {
		Wikis []struct {
			ID       string `json:"id"`
			Problem  string `json:"problem"`
			Problems int    `json:"problems"`
		} `json:"wikis"`
		Failing int `json:"failing"`
	}
	if err != errCheckFailed || json.Unmarshal([]byte(out), &v) != nil || v.Failing != 2 || len(v.Wikis) != 3 {
		t.Fatalf("check --all --json: %v\n%s", err, out)
	}
	for _, w := range v.Wikis {
		if (w.Problem == "") != (w.Problems == 0) {
			t.Errorf("wiki %s: problem %q with %d problems", w.ID, w.Problem, w.Problems)
		}
	}

	// The broken entries can be cleared, deleted repository included, and then
	// check --all passes.
	if _, err := runCLI("unbind", gone); err != nil {
		t.Fatalf("unbind a deleted repository: %v", err)
	}
	if _, err := runCLI("workspace", "remove", "base", bare); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI("check", "--all"); err != nil || !strings.Contains(out, "1 wiki(s) checked, 0 with problems") {
		t.Fatalf("check --all after cleanup: %v\n%s", err, out)
	}
	if _, err := runCLI("unbind", filepath.Join(t.TempDir(), "never-bound")); err == nil {
		t.Fatal("unbinding an unknown missing path must fail")
	}
	if _, err := runCLI("status", "--all", "--wiki", "x"); err == nil {
		t.Fatal("--all with --wiki must fail")
	}
	if _, err := runCLI("check", "--json"); err == nil {
		t.Fatal("--json without --all must fail")
	}
}
