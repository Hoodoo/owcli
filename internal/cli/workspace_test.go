package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owcli/internal/store"
)

const backoffDoc = "---\ntype: Concept\ntitle: Retry Handling\ndescription: How calls are retried.\n---\n\n# Retry Handling\n\n## Backoff policy\n\nThe client retries with exponential backoff.\n"

// workspaceRepos returns the current repository (from cliRepo) and a second
// one, each with an in-repo wiki page.
func workspaceRepos(t *testing.T) (string, string) {
	t.Helper()
	here, err := filepath.EvalSymlinks(cliRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	writePage(t, filepath.Join(here, "openwiki"), "concepts/greet.md", greetDoc)
	peer := filepath.Join(t.TempDir(), "retry-lib")
	if err := os.MkdirAll(peer, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, peer, "retry.go", "package retry\n")
	gitRun(t, peer, "init", "-q")
	commitAll(t, peer, "init")
	if peer, err = filepath.EvalSymlinks(peer); err != nil {
		t.Fatal(err)
	}
	writePage(t, filepath.Join(peer, "openwiki"), "concepts/retry.md", backoffDoc)
	return here, peer
}

func searchJSON(t *testing.T, args ...string) (map[string]any, error) {
	t.Helper()
	out, err := runCLI(append([]string{"search", "--json"}, args...)...)
	var v map[string]any
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
		t.Fatalf("search %v: not JSON (%v): %q", args, jerr, out)
	}
	return v, err
}

func TestWorkspaceSearchAndRead(t *testing.T) {
	here, peer := workspaceRepos(t)
	dirs, err := store.DefaultDirs()
	if err != nil {
		t.Fatal(err)
	}

	// Standalone: only this wiki, and no wiki field on results.
	v, err := searchJSON(t, "retry backoff")
	if err != nil || len(v["results"].([]any)) != 0 || v["workspace"] != nil {
		t.Fatalf("standalone search: %v %v", v, err)
	}

	if _, err := dirs.SaveWorkspaces([]store.WorkspaceDraft{{Name: "Emacs", Roots: []string{here, peer}}}); err != nil {
		t.Fatal(err)
	}
	v, err = searchJSON(t, "retry backoff")
	if err != nil {
		t.Fatal(err)
	}
	results := v["results"].([]any)
	if len(results) == 0 || v["workspace"].(map[string]any)["id"] != "emacs" || len(v["wikis"].([]any)) != 2 {
		t.Fatalf("workspace search: %v", v)
	}
	top := results[0].(map[string]any)
	ref := top["ref"].([]any)[0].(string)
	if top["wiki"] != "retry-lib" || ref != "openwiki/concepts/retry.md#backoff-policy" {
		t.Fatalf("top result: %v", top)
	}

	out, err := runCLI("read", "--wiki", "retry-lib", ref)
	if err != nil || !strings.Contains(out, "exponential backoff") {
		t.Fatalf("read peer: %v %q", err, out)
	}
	out, err = runCLI("read", "--json", "--wiki", "retry-lib", ref)
	if err != nil || !strings.Contains(out, `"wiki": "retry-lib"`) {
		t.Fatalf("read peer json: %v %q", err, out)
	}
	if _, err := runCLI("read", "--wiki", "unknown", ref); err == nil {
		t.Fatal("reading a wiki outside the workspace must fail")
	}

	// A second workspace makes the choice ambiguous until one is named or active.
	reg, _ := dirs.LoadWorkspaces()
	drafts := append(reg.WorkspaceDrafts(), store.WorkspaceDraft{Name: "Solo", Roots: []string{here}})
	if _, err := dirs.SaveWorkspaces(drafts); err != nil {
		t.Fatal(err)
	}
	v, err = searchJSON(t, "retry backoff")
	if err != ErrReported || v["status"] != "workspace_required" || len(v["workspaces"].([]any)) != 2 {
		t.Fatalf("ambiguous search: %v %v", v, err)
	}
	out, err = runCLI("search", "retry", "backoff")
	if err != ErrReported || !strings.Contains(out, "owcli workspace use") || !strings.Contains(out, "solo") {
		t.Fatalf("ambiguous text search: %v %q", err, out)
	}
	if v, err = searchJSON(t, "--workspace", "SOLO", "retry backoff"); err != nil || len(v["results"].([]any)) != 0 {
		t.Fatalf("explicit workspace: %v %v", v, err)
	}
	if _, err := dirs.SetActiveWorkspace(here, "emacs"); err != nil {
		t.Fatal(err)
	}
	if v, err = searchJSON(t, "retry backoff"); err != nil || len(v["results"].([]any)) == 0 {
		t.Fatalf("active workspace: %v %v", v, err)
	}
}
