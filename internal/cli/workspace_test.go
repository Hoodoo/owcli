package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hoodoo/owcli/internal/store"
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

func wsJSON(t *testing.T, args ...string) (map[string]any, error) {
	t.Helper()
	out, err := runCLI(append([]string{"workspace", "--json"}, args...)...)
	var v map[string]any
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
		t.Fatalf("workspace %v: not JSON (%v): %q", args, jerr, out)
	}
	return v, err
}

func TestWorkspaceCommands(t *testing.T) {
	here, peer := workspaceRepos(t)
	empty := filepath.Join(t.TempDir(), "kata.el")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, empty, "init", "-q")

	v, err := wsJSON(t, "create", "Emacs Packages", ".", peer)
	if err != nil {
		t.Fatal(err)
	}
	ws := v["workspace"].(map[string]any)
	if ws["id"] != "emacs-packages" || ws["wikiCount"] != 2.0 {
		t.Fatalf("create: %v", v)
	}
	if v, err = wsJSON(t, "create", "emacs packages"); err != ErrReported || v["error"].(map[string]any)["code"] != "invalid_input" {
		t.Fatalf("duplicate create: %v %v", v, err)
	}

	// A member without a wiki is listed as not searchable but still joins.
	if v, err = wsJSON(t, "add", "EMACS PACKAGES", empty); err != nil {
		t.Fatal(err)
	}
	wikis := v["workspace"].(map[string]any)["wikis"].([]any)
	if len(wikis) != 3 || !strings.Contains(wikis[2].(map[string]any)["problem"].(string), "no wiki") {
		t.Fatalf("add: %v", v)
	}
	if out, err := runCLI("workspace", "wikis", "emacs-packages"); err != nil || !strings.Contains(out, "not searchable") || !strings.Contains(out, here) {
		t.Fatalf("wikis text: %v %q", err, out)
	}

	if _, err := runCLI("workspace", "create", "Tools", "."); err != nil {
		t.Fatal(err)
	}
	if v, err = wsJSON(t, "list"); err != nil || len(v["workspaces"].([]any)) != 2 {
		t.Fatalf("list: %v %v", v, err)
	}
	if v, err = wsJSON(t, "current"); err != nil || len(v["workspaces"].([]any)) != 2 || v["activeWorkspace"] != nil {
		t.Fatalf("current: %v %v", v, err)
	}
	if v, err = wsJSON(t, "use", "tools"); err != nil || v["activeWorkspace"].(map[string]any)["id"] != "tools" {
		t.Fatalf("use: %v %v", v, err)
	}
	if out, err := runCLI("workspace", "current"); err != nil || !strings.Contains(out, "* tools") {
		t.Fatalf("current text: %v %q", err, out)
	}
	if v, err = wsJSON(t, "clear"); err != nil || v["cleared"] != true {
		t.Fatalf("clear: %v %v", v, err)
	}

	// From the wiki-less member, search covers the workspace.
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	if v, err := searchJSON(t, "retry backoff"); err != nil || len(v["results"].([]any)) == 0 || len(v["skipped"].([]any)) != 1 {
		t.Fatalf("search from wiki-less member: %v %v", v, err)
	}
	if err := os.Chdir(here); err != nil {
		t.Fatal(err)
	}

	if v, err = wsJSON(t, "remove", "emacs-packages", peer, empty); err != nil || v["workspace"].(map[string]any)["wikiCount"] != 1.0 {
		t.Fatalf("remove: %v %v", v, err)
	}
	if v, err = wsJSON(t, "remove", "emacs-packages", peer); err != ErrReported || !strings.Contains(v["error"].(map[string]any)["message"].(string), "not a member") {
		t.Fatalf("remove non-member: %v %v", v, err)
	}
	if v, err = wsJSON(t, "delete", "Tools"); err != nil || v["deleted"].(map[string]any)["id"] != "tools" {
		t.Fatalf("delete: %v %v", v, err)
	}
	if v, err = wsJSON(t, "use", "tools"); err != ErrReported || v["error"].(map[string]any)["code"] != "invalid_input" {
		t.Fatalf("use deleted: %v %v", v, err)
	}
	if _, err := os.Stat(filepath.Join(peer, "openwiki", "concepts", "retry.md")); err != nil {
		t.Fatal("workspace commands must not touch member wikis")
	}
}

func TestWikiRefsFromOutsideRepositories(t *testing.T) {
	here, peer := workspaceRepos(t)
	dirs, err := store.DefaultDirs()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dirs.SaveWorkspaces([]store.WorkspaceDraft{{Name: "Emacs", Roots: []string{here, peer}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil { // not a Git repository
		t.Fatal(err)
	}

	if _, err := runCLI("search", "retry"); err == nil || !strings.Contains(err.Error(), "not inside a Git repository") {
		t.Fatalf("untargeted search outside a repository: %v", err)
	}
	v, err := searchJSON(t, "--workspace", "emacs", "retry backoff")
	if err != nil || len(v["results"].([]any)) == 0 || v["workspace"].(map[string]any)["id"] != "emacs" {
		t.Fatalf("search --workspace from outside: %v %v", v, err)
	}
	v, err = searchJSON(t, "--wiki", "retry-lib", "retry backoff")
	if err != nil || len(v["results"].([]any)) == 0 || v["results"].([]any)[0].(map[string]any)["wiki"] != "retry-lib" || v["workspace"] != nil {
		t.Fatalf("search --wiki from outside: %v %v", v, err)
	}
	if out, err := runCLI("read", "--wiki", "retry-lib", "openwiki/concepts/retry.md#backoff-policy"); err != nil || !strings.Contains(out, "exponential backoff") {
		t.Fatalf("read --wiki from outside: %v %q", err, out)
	}
	if out, err := runCLI("status", "--wiki", "retry-lib"); err != nil || !strings.Contains(out, peer) {
		t.Fatalf("status --wiki: %v %q", err, out)
	}
	if out, err := runCLI("check", "--wiki", "retry-lib"); (err != nil && err != errCheckFailed) || strings.Contains(out, "not inside") {
		t.Fatalf("check --wiki must resolve the wiki: %v %q", err, out)
	}
	if _, err := runCLI("search", "--wiki", "retry-lib", "--workspace", "emacs", "retry"); err == nil {
		t.Fatal("--wiki and --workspace together must fail")
	}
	if _, err := runCLI("status", "--wiki", "nope"); err == nil || !strings.Contains(err.Error(), "no known wiki") {
		t.Fatalf("unknown --wiki: %v", err)
	}
}
