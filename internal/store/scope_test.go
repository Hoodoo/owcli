package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scopeFixture: a (in-repo wiki) and b (external wiki) are in Emacs; a and c
// (no wiki) are in Tools, c is in Emacs too; d has a wiki and no workspace;
// e has neither.
type scopeFixture struct {
	d              Dirs
	a, b, c, dd, e string
}

func newScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	f := scopeFixture{d: testDirs(t), a: gitRepo(t), b: gitRepo(t), c: gitRepo(t), dd: gitRepo(t), e: gitRepo(t)}
	for _, r := range []string{f.a, f.dd} {
		if err := os.MkdirAll(filepath.Join(r, WikiDirName), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.d.Bind(f.b, External, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.SaveWorkspaces([]WorkspaceDraft{
		{Name: "Emacs", Roots: []string{f.a, f.b, f.c}},
		{Name: "Tools", Roots: []string{f.a, f.c}},
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f scopeFixture) id(t *testing.T, root string) string {
	t.Helper()
	reg, err := f.d.LoadWorkspaces()
	if err != nil {
		t.Fatal(err)
	}
	w, ok := reg.WikiByRoot(root)
	if !ok {
		t.Fatalf("%s is not registered", root)
	}
	return w.ID
}

func wikiIDs(s SearchScope) string {
	var ids []string
	for _, w := range s.Wikis {
		ids = append(ids, w.ID)
	}
	return strings.Join(ids, ",")
}

func TestResolveSearchScope(t *testing.T) {
	f := newScopeFixture(t)
	a, b, c := f.id(t, f.a), f.id(t, f.b), f.id(t, f.c)

	type want struct {
		status, workspace, wikis, skipped, choices string
		err                                        error
	}
	cases := []struct {
		name, dir, requested, active string
		want                         want
	}{
		{"no workspace searches itself", f.dd, "", "", want{status: ScopeReady}},
		{"no wiki and no workspace", f.e, "", "", want{err: ErrUnbound}},
		{"single workspace is automatic", f.b, "", "", want{status: ScopeReady, workspace: "emacs", wikis: a + "," + b, skipped: c}},
		{"several without active", f.a, "", "", want{status: ScopeWorkspaceRequired, choices: "emacs,tools"}},
		{"several with active", f.a, "", "tools", want{status: ScopeReady, workspace: "tools", wikis: a, skipped: c}},
		{"explicit by name ignoring case", f.a, "EMACS", "tools", want{status: ScopeReady, workspace: "emacs", wikis: a + "," + b, skipped: c}},
		{"explicit workspace not containing repo", f.b, "tools", "", want{err: ErrWorkspace}},
		{"explicit unknown workspace", f.b, "nope", "", want{err: ErrWorkspace}},
		{"member without its own wiki", f.c, "", "emacs", want{status: ScopeReady, workspace: "emacs", wikis: a + "," + b, skipped: c}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.d.ClearActiveWorkspace(tc.dir); err != nil {
				t.Fatal(err)
			}
			if tc.active != "" {
				if _, err := f.d.SetActiveWorkspace(tc.dir, tc.active); err != nil {
					t.Fatal(err)
				}
			}
			s, err := f.d.ResolveSearchScope(tc.dir, tc.requested)
			if tc.want.err != nil {
				if !errors.Is(err, tc.want.err) {
					t.Fatalf("err = %v, want %v", err, tc.want.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ws := ""
			if s.Workspace != nil {
				ws = s.Workspace.ID
			}
			var skipped, choices []string
			for _, m := range s.Skipped {
				skipped = append(skipped, m.Wiki.ID)
			}
			for _, w := range s.Choices {
				choices = append(choices, w.ID)
			}
			got := want{status: s.Status, workspace: ws, wikis: wikiIDs(s), skipped: strings.Join(skipped, ","), choices: strings.Join(choices, ",")}
			if tc.dir == f.dd {
				// A standalone wiki has no registry ID; check the layout instead.
				if len(s.Wikis) != 1 || s.Wikis[0].Layout.RepoRoot != f.dd || s.Current.ID != "" {
					t.Fatalf("standalone scope: %+v", s)
				}
				got.wikis = ""
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestResolveReadableWiki(t *testing.T) {
	f := newScopeFixture(t)
	a, b, c := f.id(t, f.a), f.id(t, f.b), f.id(t, f.c)
	cases := []struct {
		name, dir, wiki, wantRoot string
		err                       error
	}{
		{"own wiki", f.a, "", f.a, nil},
		{"own wiki by id", f.a, a, f.a, nil},
		{"standalone own wiki", f.dd, "", f.dd, nil},
		{"member without a wiki reads its own", f.c, "", "", ErrUnbound},
		{"member without a wiki reads a peer", f.c, b, f.b, nil},
		{"external peer reads in-repo wiki", f.b, a, f.a, nil},
		{"peer without a wiki", f.b, c, "", ErrWorkspace},
		{"no shared workspace", f.dd, a, "", ErrWorkspace},
		{"unknown id", f.a, "nope", "", ErrWorkspace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, err := f.d.ResolveReadableWiki(tc.dir, tc.wiki)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil || w.Layout.RepoRoot != tc.wantRoot {
				t.Fatalf("got %+v %v, want root %s", w, err, tc.wantRoot)
			}
		})
	}
}
