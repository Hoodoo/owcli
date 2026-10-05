package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// moveRepo renames a repository directory and returns its new canonical root.
func moveRepo(t *testing.T, root, to string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, to); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(to)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestRelocateRewritesBindingsAndWorkspaces(t *testing.T) {
	d := testDirs(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(base, "old")
	shop := moveRepo(t, gitRepo(t), filepath.Join(oldDir, "shop"))
	kb := moveRepo(t, gitRepo(t), filepath.Join(oldDir, "kb"))
	other := gitRepo(t)

	if _, err := d.Bind(shop, InRepo, "", now); err != nil {
		t.Fatal(err)
	}
	// An external wiki kept in a directory under the moved tree.
	if _, err := d.Bind(kb, External, filepath.Join(oldDir, "wikis", "kb"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Bind(other, InRepo, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SaveWorkspaces([]WorkspaceDraft{{Name: "shop", Roots: []string{shop, other}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetActiveWorkspace(shop, "shop"); err != nil {
		t.Fatal(err)
	}

	newDir := filepath.Join(base, "new")
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatal(err)
	}

	before, _ := os.ReadFile(d.registryPath())
	changes, err := d.Relocate(oldDir, newDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 4 {
		t.Fatalf("dry run changes = %+v", changes)
	}
	if after, _ := os.ReadFile(d.registryPath()); string(after) != string(before) {
		t.Fatal("dry run wrote bindings.json")
	}

	changes, err = d.Relocate(oldDir, newDir, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range changes {
		if !c.Exists {
			t.Errorf("%s should exist after the move", c.To)
		}
		got = append(got, c.Registry+"."+c.Field+"="+strings.TrimPrefix(c.To, newDir))
	}
	want := "bindings.repoRoot=/kb,bindings.wikiDir=/wikis/kb,bindings.repoRoot=/shop,workspaces.root=/shop"
	if strings.Join(got, ",") != want {
		t.Errorf("changes = %s, want %s", strings.Join(got, ","), want)
	}

	l, err := d.Resolve(filepath.Join(newDir, "kb"))
	if err != nil || l.Kind != External || l.Home != filepath.Join(newDir, "wikis", "kb") {
		t.Fatalf("external binding after relocate: %+v, %v", l, err)
	}
	reg, err := d.LoadWorkspaces()
	if err != nil {
		t.Fatal(err)
	}
	w, ok := reg.WikiByRoot(filepath.Join(newDir, "shop"))
	if !ok {
		t.Fatal("workspace member not relocated")
	}
	if active, ok := reg.ActiveFor(w.ID); !ok || active != "shop" {
		t.Errorf("active selection lost: %q %v", active, ok)
	}
	if _, ok := reg.WikiByRoot(other); !ok {
		t.Error("a wiki outside the old path must keep its root")
	}

	if changes, err := d.Relocate(oldDir, newDir, false); err != nil || len(changes) != 0 {
		t.Errorf("second relocate: %+v, %v", changes, err)
	}
}

func TestRelocateRefusesCollisions(t *testing.T) {
	d := testDirs(t)
	a, b := gitRepo(t), gitRepo(t)
	for _, r := range []string{a, b} {
		if _, err := d.Bind(r, InRepo, "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadFile(d.registryPath())
	if _, err := d.Relocate(a, b, false); err == nil || !strings.Contains(err.Error(), "bound twice") {
		t.Fatalf("want collision error, got %v", err)
	}
	if after, _ := os.ReadFile(d.registryPath()); string(after) != string(before) {
		t.Fatal("a refused relocate wrote bindings.json")
	}
	if _, err := d.Relocate(a, a, false); err == nil {
		t.Fatal("relocating a path onto itself must fail")
	}
}
