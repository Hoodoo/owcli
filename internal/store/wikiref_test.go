package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// namedRepo makes a Git repository whose directory is called name.
func namedRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolveWikiRef(t *testing.T) {
	d := testDirs(t)
	app, lib1, lib2, kb, bare := namedRepo(t, "app"), namedRepo(t, "lib"), namedRepo(t, "lib"), namedRepo(t, "kb"), namedRepo(t, "bare")
	for _, r := range []string{app, lib1, lib2} {
		if _, err := d.Bind(r, External, "", now); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(kb, WikiDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SaveWorkspaces([]WorkspaceDraft{{Name: "Base", Roots: []string{kb, bare}}}); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	cases := []struct {
		name, ref, wantRoot, wantID, errHas string
	}{
		{"unique repository name", "app", app, WikiID(app), ""},
		{"name slugs alike", "APP", app, WikiID(app), ""},
		{"wiki ID", WikiID(lib2), lib2, WikiID(lib2), ""},
		{"ambiguous name lists IDs", "lib", "", "", WikiID(lib1)},
		{"workspace member by its workspace ID", "kb", kb, "kb", ""},
		{"workspace member by WikiID", WikiID(kb), kb, "kb", ""},
		{"member without a wiki", "bare", "", "", "has no wiki"},
		{"unknown", "nope", "", "", "no known wiki"},
		{"empty", " ", "", "", "give a wiki"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, err := d.ResolveWikiRef(tc.ref)
			if tc.errHas != "" {
				if !errors.Is(err, ErrWorkspace) || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want ErrWorkspace containing %q", err, tc.errHas)
				}
				return
			}
			if err != nil || w.Layout.RepoRoot != tc.wantRoot || w.ID != tc.wantID {
				t.Fatalf("got %+v %v, want root %s id %s", w, err, tc.wantRoot, tc.wantID)
			}
		})
	}

	// A registry entry whose repository is gone is reported, not resolved.
	if _, err := d.SaveWorkspaces([]WorkspaceDraft{{Name: "Base", Roots: []string{kb, bare, gone}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ResolveWikiRef("gone"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing repository: %v", err)
	}
}

func TestScopeWithoutCurrentWiki(t *testing.T) {
	f := newScopeFixture(t)
	outside := t.TempDir()                       // not a Git repository
	for _, dir := range []string{outside, f.e} { // f.e: a repository with no wiki, in no workspace
		if _, err := f.d.ResolveSearchScope(dir, ""); err == nil {
			t.Errorf("%s: a search with no target must fail", dir)
		}
		s, err := f.d.ResolveSearchScope(dir, "tools")
		if err != nil || s.Workspace == nil || s.Workspace.ID != "tools" || s.Current.ID != "" {
			t.Fatalf("%s: explicit workspace without a current wiki: %+v %v", dir, s, err)
		}
		if _, err := f.d.ResolveReadableWiki(dir, ""); err == nil {
			t.Errorf("%s: a read with no target must fail", dir)
		}
		if w, err := f.d.ResolveReadableWiki(dir, f.id(t, f.b)); err != nil || w.Layout.RepoRoot != f.b {
			t.Fatalf("%s: read by ID without a current wiki: %+v %v", dir, w, err)
		}
	}
	// Inside a repository with a wiki, upstream's restriction still applies.
	if _, err := f.d.ResolveSearchScope(f.b, "tools"); !errors.Is(err, ErrWorkspace) {
		t.Fatalf("explicit workspace not containing the current wiki: %v", err)
	}
	if _, err := f.d.ResolveReadableWiki(f.dd, f.id(t, f.a)); !errors.Is(err, ErrWorkspace) {
		t.Fatalf("read of an unrelated wiki from a wiki repository: %v", err)
	}
}
