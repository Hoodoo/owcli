package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspacesMissingFileIsEmpty(t *testing.T) {
	reg, err := testDirs(t).LoadWorkspaces()
	if err != nil || reg.Version != 1 || len(reg.Workspaces) != 0 {
		t.Fatalf("empty registry: %+v %v", reg, err)
	}
}

func TestSaveWorkspacesAssignsStableIDs(t *testing.T) {
	d := testDirs(t)
	reg, err := d.SaveWorkspaces([]WorkspaceDraft{
		{Name: "Emacs Packages", Roots: []string{"/src/vui-workitem", "/src/kata.el"}},
		{Name: "Emacs packages 2", Roots: []string{"/src/vui-workitem"}},
		{Name: "!!!", Roots: []string{"/other/kata.el"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := func(r WorkspaceRegistry) (ws, wikis []string) {
		for _, w := range r.Workspaces {
			ws = append(ws, w.ID)
		}
		for _, w := range r.Wikis {
			wikis = append(wikis, w.ID+"="+w.Root)
		}
		return
	}
	ws, wikis := ids(reg)
	if strings.Join(ws, ",") != "emacs-packages,emacs-packages-2,workspace" {
		t.Errorf("workspace ids = %v", ws)
	}
	if strings.Join(wikis, ",") != "kata-el=/src/kata.el,kata-el-2=/other/kata.el,vui-workitem=/src/vui-workitem" {
		t.Errorf("wiki ids = %v", wikis)
	}

	// Renaming a workspace and dropping a member keeps the surviving IDs; a new
	// workspace whose slug collides with a kept ID gets a suffix.
	drafts := reg.WorkspaceDrafts()
	drafts[0].Name = "Elisp"
	drafts[0].Roots = []string{"/src/kata.el"}
	drafts = append(drafts, WorkspaceDraft{Name: "Emacs packages", Roots: []string{"/src/kata.el"}})
	reg, err = d.SaveWorkspaces(drafts)
	if err != nil {
		t.Fatal(err)
	}
	ws, wikis = ids(reg)
	if strings.Join(ws, ",") != "emacs-packages,emacs-packages-2,workspace,emacs-packages-3" || reg.Workspaces[0].Name != "Elisp" {
		t.Errorf("after edit: workspaces %v (%s)", ws, reg.Workspaces[0].Name)
	}
	if strings.Join(wikis, ",") != "kata-el=/src/kata.el,kata-el-2=/other/kata.el,vui-workitem=/src/vui-workitem" {
		t.Errorf("after edit: wikis %v", wikis)
	}

	again, err := d.LoadWorkspaces()
	if err != nil || len(again.Workspaces) != 4 {
		t.Fatalf("reload: %+v %v", again, err)
	}
}

func TestSaveWorkspacesRejectsBadDrafts(t *testing.T) {
	d := testDirs(t)
	for name, drafts := range map[string][]WorkspaceDraft{
		"empty name":     {{Name: "  "}},
		"long name":      {{Name: strings.Repeat("x", MaxWorkspaceName+1)}},
		"duplicate name": {{Name: "A"}, {Name: "a"}},
		"unknown id":     {{ID: "nope", Name: "A"}},
		"relative root":  {{Name: "A", Roots: []string{"src/x"}}},
	} {
		if _, err := d.SaveWorkspaces(drafts); !errors.Is(err, ErrWorkspace) {
			t.Errorf("%s: err = %v, want ErrWorkspace", name, err)
		}
	}
	if _, err := os.Stat(d.workspacesPath()); !os.IsNotExist(err) {
		t.Errorf("rejected drafts must not write the registry: %v", err)
	}
}

func TestLoadWorkspacesIsStrict(t *testing.T) {
	d := testDirs(t)
	for name, content := range map[string]string{
		"version":        `{"version": 2, "wikis": [], "workspaces": [], "active": []}`,
		"unknown field":  `{"version": 1, "wikis": [], "workspaces": [], "active": [], "extra": 1}`,
		"unknown member": `{"version": 1, "wikis": [], "workspaces": [{"id": "a", "name": "A", "wikis": ["x"]}], "active": []}`,
		"bad active":     `{"version": 1, "wikis": [{"id": "x", "name": "x", "root": "/x"}], "workspaces": [{"id": "a", "name": "A", "wikis": []}], "active": [{"wiki": "x", "workspace": "a"}]}`,
		"dup name":       `{"version": 1, "wikis": [], "workspaces": [{"id": "a", "name": "A", "wikis": []}, {"id": "b", "name": "a", "wikis": []}], "active": []}`,
	} {
		if err := os.MkdirAll(d.Config, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d.workspacesPath(), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := d.LoadWorkspaces(); !errors.Is(err, ErrInvalidState) {
			t.Errorf("%s: err = %v, want ErrInvalidState", name, err)
		}
	}
}

func TestActiveWorkspaceSelection(t *testing.T) {
	d := testDirs(t)
	repo, other := gitRepo(t), gitRepo(t)
	reg, err := d.SaveWorkspaces([]WorkspaceDraft{
		{Name: "One", Roots: []string{repo, other}},
		{Name: "Two", Roots: []string{repo}},
		{Name: "Three", Roots: []string{other}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetActiveWorkspace(repo, "three"); !errors.Is(err, ErrWorkspace) {
		t.Fatalf("activating a workspace the repo is not in: %v", err)
	}
	if _, err := d.SetActiveWorkspace(repo, "missing"); !errors.Is(err, ErrWorkspace) {
		t.Fatalf("activating an unknown workspace: %v", err)
	}
	if ws, err := d.SetActiveWorkspace(filepath.Join(repo, "."), "TWO"); err != nil || ws.ID != "two" {
		t.Fatalf("activate by name ignoring case: %+v %v", ws, err)
	}
	if _, err := d.SetActiveWorkspace(other, "one"); err != nil {
		t.Fatal(err)
	}
	reg, _ = d.LoadWorkspaces()
	w, _ := reg.WikiByRoot(repo)
	if got, ok := reg.ActiveFor(w.ID); !ok || got != "two" {
		t.Fatalf("active for repo = %q %v", got, ok)
	}

	// Removing repo from Two invalidates its selection; other's survives.
	drafts := reg.WorkspaceDrafts()
	drafts[1].Roots = nil
	if reg, err = d.SaveWorkspaces(drafts); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.ActiveFor(w.ID); ok {
		t.Error("selection of a removed membership must be dropped")
	}
	o, _ := reg.WikiByRoot(other)
	if got, _ := reg.ActiveFor(o.ID); got != "one" {
		t.Errorf("unrelated selection lost: %q", got)
	}

	if removed, err := d.ClearActiveWorkspace(other); err != nil || !removed {
		t.Fatalf("clear: %v %v", removed, err)
	}
	if removed, err := d.ClearActiveWorkspace(other); err != nil || removed {
		t.Fatalf("second clear: %v %v", removed, err)
	}
}

func TestResolveMembersReportsProblems(t *testing.T) {
	d := testDirs(t)
	inRepo, external, unbound := gitRepo(t), gitRepo(t), gitRepo(t)
	if _, err := d.Bind(inRepo, InRepo, "", now); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(inRepo, WikiDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	ext, err := d.Bind(external, External, "", now)
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	reg, err := d.SaveWorkspaces([]WorkspaceDraft{{Name: "All", Roots: []string{inRepo, external, unbound, gone}}})
	if err != nil {
		t.Fatal(err)
	}
	members, err := d.ResolveMembers(reg, reg.Workspaces[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 4 {
		t.Fatalf("members = %+v", members)
	}
	if members[0].Problem != "" || members[0].WikiDir != filepath.Join(inRepo, WikiDirName) {
		t.Errorf("in-repo member: %+v", members[0])
	}
	if members[1].Problem != "" || members[1].WikiDir != ext.WikiRoot {
		t.Errorf("external member: %+v", members[1])
	}
	if !strings.Contains(members[2].Problem, "no wiki") {
		t.Errorf("unbound member: %+v", members[2])
	}
	if !strings.Contains(members[3].Problem, "not found") {
		t.Errorf("missing repo: %+v", members[3])
	}
}
