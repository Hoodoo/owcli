package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChdirFlag runs each command family from outside the repository with -C.
func TestChdirFlag(t *testing.T) {
	repo, err := filepath.EvalSymlinks(cliRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	writePage(t, filepath.Join(repo, "openwiki"), "concepts/greet.md", greetDoc)
	outside := t.TempDir()
	// Each command chdirs into the repository; start every one from outside.
	run := func(args ...string) (string, error) {
		t.Helper()
		if err := os.Chdir(outside); err != nil {
			t.Fatal(err)
		}
		return runCLI(append([]string{"-C", repo}, args...)...)
	}

	out, err := run("search", "--json", "greet")
	var res struct {
		Results []struct{ Ref []string } `json:"results"`
	}
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || len(res.Results) == 0 {
		t.Fatalf("search: %v %q", err, out)
	}
	if out, err = run("read", res.Results[0].Ref[0]); err != nil || !strings.Contains(out, "prints hello") {
		t.Fatalf("read: %v %q", err, out)
	}
	if out, err = run("status"); err != nil || !strings.Contains(out, repo) {
		t.Fatalf("status: %v %q", err, out)
	}
	if out, err = run("run", "next"); err == nil || !strings.Contains(out, `"not_found"`) {
		t.Fatalf("run next without a run must resolve the repo and report not_found: %v %q", err, out)
	}
	if out, err = run("workspace", "current", "--json"); err != nil || !strings.Contains(out, filepath.Base(repo)) {
		t.Fatalf("workspace current: %v %q", err, out)
	}
	if _, err = run("agents-md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); err != nil {
		t.Fatalf("agents-md wrote outside the repository: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("agents-md wrote into the real working directory")
	}

	// Relative paths in other arguments resolve from the -C directory, as with git.
	if err := os.Chdir(outside); err != nil {
		t.Fatal(err)
	}
	out, err = runCLI("-C", filepath.Dir(repo), "workspace", "create", "--json", "Rel", filepath.Base(repo))
	if err != nil || !strings.Contains(out, `"root": "`+repo+`"`) {
		t.Fatalf("relative member path: %v %q", err, out)
	}

	missing := filepath.Join(outside, "missing")
	if _, err := runCLI("-C", missing, "status"); err == nil || !strings.Contains(err.Error(), "-C "+missing) {
		t.Fatalf("bad -C: %v", err)
	}
	if out, err := runCLI("-C", missing, "run", "next"); err != ErrReported || !strings.Contains(out, `"invalid_input"`) {
		t.Fatalf("bad -C under run must print a JSON error: %v %q", err, out)
	}
}
