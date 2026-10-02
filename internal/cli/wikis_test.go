package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"owcli/internal/store"
)

// TestWikisListing checks the `owcli wikis --json` contract: every bound
// repository and workspace member, membership on both sides, and problems.
func TestWikisListing(t *testing.T) {
	bound, err := filepath.EvalSymlinks(cliRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bound, "openwiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	newRepo := func(name string, wiki bool) string {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		gitRun(t, dir, "init", "-q")
		if wiki {
			if err := os.MkdirAll(filepath.Join(dir, "openwiki"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		root, _ := filepath.EvalSymlinks(dir)
		return root
	}
	ext, memberOnly, bare := newRepo("ext", false), newRepo("lib", true), newRepo("bare", false)
	for _, args := range [][]string{{"bind", bound}, {"bind", "--external", ext}} {
		if _, err := runCLI(args...); err != nil {
			t.Fatal(err)
		}
	}
	dirs, _ := store.DefaultDirs()
	if _, err := dirs.SaveWorkspaces([]store.WorkspaceDraft{
		{Name: "Emacs", Roots: []string{bound, memberOnly, bare}},
		{Name: "Tools", Roots: []string{bound}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dirs.SetActiveWorkspace(bound, "tools"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI("wikis", "--json")
	var l wikisListing
	if err != nil || json.Unmarshal([]byte(out), &l) != nil {
		t.Fatalf("wikis --json: %v %q", err, out)
	}
	if len(l.Wikis) != 4 || len(l.Workspaces) != 2 {
		t.Fatalf("listing: %+v", l)
	}
	byRoot := map[string]wikiEntry{}
	for _, e := range l.Wikis {
		byRoot[e.RepoRoot] = e
	}
	b := byRoot[bound]
	if !b.Bound || b.Layout != store.InRepo || len(b.Workspaces) != 2 || b.Health != nil {
		t.Errorf("bound wiki: %+v", b)
	}
	for _, w := range b.Workspaces {
		if w.Active != (w.ID == "tools") {
			t.Errorf("active marker: %+v", b.Workspaces)
		}
	}
	if e := byRoot[ext]; !e.Bound || e.Layout != store.External || e.ID != store.WikiID(ext) || len(e.Workspaces) != 0 {
		t.Errorf("external wiki: %+v", e)
	}
	if e := byRoot[memberOnly]; e.Bound || e.ID != "lib" || e.Problem != "" || e.WikiDir == "" {
		t.Errorf("member-only wiki: %+v", e)
	}
	if e := byRoot[bare]; !strings.Contains(e.Problem, "has no wiki") || e.WikiDir != "" {
		t.Errorf("member without a wiki: %+v", e)
	}
	for _, ws := range l.Workspaces {
		if ws.ID == "emacs" && strings.Join(ws.Wikis, ",") != b.ID+",lib,bare" {
			t.Errorf("workspace members: %+v", ws)
		}
	}

	out, err = runCLI("wikis", "--json", "--health")
	if err != nil || json.Unmarshal([]byte(out), &l) != nil {
		t.Fatalf("wikis --health: %v %q", err, out)
	}
	for _, e := range l.Wikis {
		if e.Health == nil || (e.Problem != "") != (e.Health.Problem != "") {
			t.Errorf("health for %s: %+v", e.ID, e.Health)
		}
	}

	out, err = runCLI("wikis")
	if err != nil || !strings.Contains(out, "tools*") || !strings.Contains(out, "workspace member only") || !strings.Contains(out, "unavailable:") || !strings.Contains(out, "\nworkspaces:\n") {
		t.Fatalf("wikis text: %v\n%s", err, out)
	}
}
