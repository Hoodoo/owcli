package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The workspace registry groups repository wikis into named workspaces for
// federated search. Its schema matches upstream's wiki-workspaces.json, but it
// is owcli's own file: members are repository roots resolved through owcli
// bindings, so external wikis can join, and upstream's file is never written.

const workspacesSchemaVersion = 1

// MaxWorkspaceName bounds workspace names and references, as upstream does.
const MaxWorkspaceName = 80

const maxSlug = 56

// ErrWorkspace marks a workspace request that is wrong as asked (unknown
// name, duplicate, wiki not a member); the message is safe to show as is.
var ErrWorkspace = errors.New("workspace")

// RegisteredWiki is one repository wiki that belongs to a workspace.
type RegisteredWiki struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Root string `json:"root"` // canonical repository root
}

// Workspace is one named set of wikis, by wiki ID.
type Workspace struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Wikis []string `json:"wikis"`
}

// ActiveWorkspace is one repository's persistent workspace selection.
type ActiveWorkspace struct {
	Wiki      string `json:"wiki"`
	Workspace string `json:"workspace"`
}

// WorkspaceRegistry is the persisted workspaces.json.
type WorkspaceRegistry struct {
	Version    int               `json:"version"`
	Wikis      []RegisteredWiki  `json:"wikis"`
	Workspaces []Workspace       `json:"workspaces"`
	Active     []ActiveWorkspace `json:"active"`
}

// WorkspaceDraft is the editable form of one workspace: SaveWorkspaces takes
// the complete collection of drafts and replaces the registry with it.
type WorkspaceDraft struct {
	ID    string   // existing workspace ID; empty for a new workspace
	Name  string   // display name, unique ignoring case
	Roots []string // canonical repository roots
}

func (d Dirs) workspacesPath() string { return filepath.Join(d.Config, "workspaces.json") }

func workspaceErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrWorkspace, fmt.Sprintf(format, args...))
}

// LoadWorkspaces reads and strictly validates the registry; a missing file
// is an empty registry.
func (d Dirs) LoadWorkspaces() (WorkspaceRegistry, error) {
	path := d.workspacesPath()
	reg := WorkspaceRegistry{Version: workspacesSchemaVersion}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return reg, nil
	}
	if err != nil {
		return WorkspaceRegistry{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reg); err != nil {
		return WorkspaceRegistry{}, invalid(path, "%v", err)
	}
	if dec.More() {
		return WorkspaceRegistry{}, invalid(path, "trailing data")
	}
	if err := reg.validate(); err != nil {
		return WorkspaceRegistry{}, invalid(path, "%v", err)
	}
	return reg, nil
}

func (r WorkspaceRegistry) validate() error {
	if r.Version != workspacesSchemaVersion {
		return fmt.Errorf("unsupported version %d", r.Version)
	}
	wikis := map[string]bool{}
	roots := map[string]bool{}
	for _, w := range r.Wikis {
		switch {
		case w.ID == "" || w.Name == "":
			return fmt.Errorf("wiki %q needs an id and a name", w.Root)
		case !filepath.IsAbs(w.Root):
			return fmt.Errorf("wiki %s root %q is not absolute", w.ID, w.Root)
		case wikis[w.ID]:
			return fmt.Errorf("duplicate wiki id %s", w.ID)
		case roots[w.Root]:
			return fmt.Errorf("duplicate wiki root %s", w.Root)
		}
		wikis[w.ID], roots[w.Root] = true, true
	}
	ids := map[string]bool{}
	names := map[string]bool{}
	members := map[string]map[string]bool{}
	for _, ws := range r.Workspaces {
		key := strings.ToLower(ws.Name)
		switch {
		case ws.ID == "" || strings.TrimSpace(ws.Name) == "":
			return fmt.Errorf("workspace %q needs an id and a name", ws.Name)
		case ids[ws.ID]:
			return fmt.Errorf("duplicate workspace id %s", ws.ID)
		case names[key]:
			return fmt.Errorf("duplicate workspace name %q", ws.Name)
		}
		ids[ws.ID], names[key] = true, true
		members[ws.ID] = map[string]bool{}
		for _, id := range ws.Wikis {
			if !wikis[id] {
				return fmt.Errorf("workspace %s lists unknown wiki %s", ws.ID, id)
			}
			if members[ws.ID][id] {
				return fmt.Errorf("workspace %s lists wiki %s twice", ws.ID, id)
			}
			members[ws.ID][id] = true
		}
	}
	seen := map[string]bool{}
	for _, a := range r.Active {
		switch {
		case !wikis[a.Wiki]:
			return fmt.Errorf("active selection for unknown wiki %s", a.Wiki)
		case members[a.Workspace] == nil:
			return fmt.Errorf("active selection names unknown workspace %s", a.Workspace)
		case !members[a.Workspace][a.Wiki]:
			return fmt.Errorf("active workspace %s does not contain wiki %s", a.Workspace, a.Wiki)
		case seen[a.Wiki]:
			return fmt.Errorf("wiki %s has two active selections", a.Wiki)
		}
		seen[a.Wiki] = true
	}
	return nil
}

// WorkspaceDrafts converts the registry into editable drafts, in registry
// order, with members as repository roots.
func (r WorkspaceRegistry) WorkspaceDrafts() []WorkspaceDraft {
	roots := map[string]string{}
	for _, w := range r.Wikis {
		roots[w.ID] = w.Root
	}
	drafts := make([]WorkspaceDraft, 0, len(r.Workspaces))
	for _, ws := range r.Workspaces {
		d := WorkspaceDraft{ID: ws.ID, Name: ws.Name}
		for _, id := range ws.Wikis {
			d.Roots = append(d.Roots, roots[id])
		}
		drafts = append(drafts, d)
	}
	return drafts
}

// SaveWorkspaces atomically replaces the workspace collection with drafts.
// Wiki and workspace IDs are kept while their repository root or workspace ID
// remains; new ones are slugs of the name made unique with -2, -3, ... The
// wiki inventory keeps only wikis some workspace still lists, and active
// selections the edit invalidates are dropped.
func (d Dirs) SaveWorkspaces(drafts []WorkspaceDraft) (WorkspaceRegistry, error) {
	old, err := d.LoadWorkspaces()
	if err != nil {
		return WorkspaceRegistry{}, err
	}
	next, err := old.apply(drafts)
	if err != nil {
		return WorkspaceRegistry{}, err
	}
	if err := WriteJSONAtomic(d.workspacesPath(), next); err != nil {
		return WorkspaceRegistry{}, err
	}
	return next, nil
}

func (r WorkspaceRegistry) apply(drafts []WorkspaceDraft) (WorkspaceRegistry, error) {
	oldWS := map[string]bool{}
	for _, ws := range r.Workspaces {
		oldWS[ws.ID] = true
	}
	wikiByRoot := map[string]RegisteredWiki{}
	usedWiki := map[string]bool{}
	for _, w := range r.Wikis {
		wikiByRoot[w.Root] = w
	}

	// Keep the IDs of wikis and workspaces that survive before naming new ones,
	// so a new entry never takes an ID an existing one still holds.
	surviving := map[string]bool{}
	usedWS := map[string]bool{}
	names := map[string]bool{}
	for _, dr := range drafts {
		name := strings.TrimSpace(dr.Name)
		switch {
		case name == "":
			return WorkspaceRegistry{}, workspaceErr("a workspace needs a name")
		case len(name) > MaxWorkspaceName:
			return WorkspaceRegistry{}, workspaceErr("workspace name %q is longer than %d characters", name, MaxWorkspaceName)
		case names[strings.ToLower(name)]:
			return WorkspaceRegistry{}, workspaceErr("workspace name %q is used twice", name)
		case dr.ID != "" && !oldWS[dr.ID]:
			return WorkspaceRegistry{}, workspaceErr("unknown workspace id %s", dr.ID)
		case dr.ID != "" && usedWS[dr.ID]:
			return WorkspaceRegistry{}, workspaceErr("workspace id %s is used twice", dr.ID)
		}
		names[strings.ToLower(name)] = true
		if dr.ID != "" {
			usedWS[dr.ID] = true
		}
		for _, root := range dr.Roots {
			if !filepath.IsAbs(root) {
				return WorkspaceRegistry{}, workspaceErr("repository root %q is not absolute", root)
			}
			if w, ok := wikiByRoot[root]; ok {
				surviving[root] = true
				usedWiki[w.ID] = true
			}
		}
	}

	next := WorkspaceRegistry{Version: workspacesSchemaVersion, Wikis: []RegisteredWiki{}, Workspaces: []Workspace{}, Active: []ActiveWorkspace{}}
	listed := map[string]bool{}
	for _, dr := range drafts {
		ws := Workspace{ID: dr.ID, Name: strings.TrimSpace(dr.Name), Wikis: []string{}}
		if ws.ID == "" {
			ws.ID = uniqueID(slugID(ws.Name, "workspace"), usedWS)
		}
		inWS := map[string]bool{}
		for _, root := range dr.Roots {
			w, ok := wikiByRoot[root]
			if !ok {
				name := filepath.Base(root)
				w = RegisteredWiki{ID: uniqueID(slugID(name, "wiki"), usedWiki), Name: name, Root: root}
				wikiByRoot[root] = w
			}
			if inWS[w.ID] {
				continue
			}
			inWS[w.ID] = true
			ws.Wikis = append(ws.Wikis, w.ID)
			if !listed[root] {
				listed[root] = true
				next.Wikis = append(next.Wikis, w)
			}
		}
		next.Workspaces = append(next.Workspaces, ws)
	}
	sort.Slice(next.Wikis, func(i, j int) bool { return next.Wikis[i].ID < next.Wikis[j].ID })

	members := map[string]map[string]bool{}
	for _, ws := range next.Workspaces {
		members[ws.ID] = map[string]bool{}
		for _, id := range ws.Wikis {
			members[ws.ID][id] = true
		}
	}
	for _, a := range r.Active {
		if members[a.Workspace][a.Wiki] {
			next.Active = append(next.Active, a)
		}
	}
	return next, next.validate()
}

// slugID makes a bounded lowercase identifier from a display name.
func slugID(name, fallback string) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(slug) > maxSlug {
		slug = strings.TrimRight(slug[:maxSlug], "-")
	}
	if slug == "" {
		return fallback
	}
	return slug
}

// uniqueID returns preferred, or preferred-N with the smallest free N >= 2,
// and marks the result used.
func uniqueID(preferred string, used map[string]bool) string {
	id := preferred
	for n := 2; used[id]; n++ {
		id = preferred + "-" + strconv.Itoa(n)
	}
	used[id] = true
	return id
}

// FindWorkspace returns the workspace whose ID matches ref exactly or whose
// name matches it ignoring case.
func (r WorkspaceRegistry) FindWorkspace(ref string) (Workspace, error) {
	ref = strings.TrimSpace(ref)
	for _, ws := range r.Workspaces {
		if ws.ID == ref {
			return ws, nil
		}
	}
	for _, ws := range r.Workspaces {
		if strings.EqualFold(ws.Name, ref) {
			return ws, nil
		}
	}
	return Workspace{}, workspaceErr("no workspace %q; list them with `owcli workspace list`", ref)
}

// WikiByRoot returns the registered wiki for a canonical repository root.
func (r WorkspaceRegistry) WikiByRoot(root string) (RegisteredWiki, bool) {
	for _, w := range r.Wikis {
		if w.Root == root {
			return w, true
		}
	}
	return RegisteredWiki{}, false
}

// WikiByID returns the registered wiki with the given ID.
func (r WorkspaceRegistry) WikiByID(id string) (RegisteredWiki, bool) {
	for _, w := range r.Wikis {
		if w.ID == id {
			return w, true
		}
	}
	return RegisteredWiki{}, false
}

// ActiveFor returns the active workspace ID for a wiki, if one is set.
func (r WorkspaceRegistry) ActiveFor(wikiID string) (string, bool) {
	for _, a := range r.Active {
		if a.Wiki == wikiID {
			return a.Workspace, true
		}
	}
	return "", false
}

// SetActiveWorkspace makes ref the active workspace of the repository
// containing dir, which must be one of its members.
func (d Dirs) SetActiveWorkspace(dir, ref string) (Workspace, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return Workspace{}, err
	}
	reg, err := d.LoadWorkspaces()
	if err != nil {
		return Workspace{}, err
	}
	ws, err := reg.FindWorkspace(ref)
	if err != nil {
		return Workspace{}, err
	}
	w, ok := reg.WikiByRoot(root)
	if !ok || !contains(ws.Wikis, w.ID) {
		return Workspace{}, workspaceErr("%s is not a member of workspace %s", root, ws.Name)
	}
	active := []ActiveWorkspace{{Wiki: w.ID, Workspace: ws.ID}}
	for _, a := range reg.Active {
		if a.Wiki != w.ID {
			active = append(active, a)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Wiki < active[j].Wiki })
	reg.Active = active
	return ws, WriteJSONAtomic(d.workspacesPath(), reg)
}

// ClearActiveWorkspace removes the active selection of the repository
// containing dir and reports whether there was one.
func (d Dirs) ClearActiveWorkspace(dir string) (bool, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return false, err
	}
	reg, err := d.LoadWorkspaces()
	if err != nil {
		return false, err
	}
	w, ok := reg.WikiByRoot(root)
	if !ok {
		return false, nil
	}
	kept := reg.Active[:0]
	removed := false
	for _, a := range reg.Active {
		if a.Wiki == w.ID {
			removed = true
			continue
		}
		kept = append(kept, a)
	}
	if !removed {
		return false, nil
	}
	reg.Active = kept
	return true, WriteJSONAtomic(d.workspacesPath(), reg)
}

// WorkspaceMember is one wiki of a workspace resolved through the bindings.
// Problem explains why a member cannot be searched; Layout is valid only when
// Problem is empty.
type WorkspaceMember struct {
	Wiki    RegisteredWiki `json:"wiki"`
	Layout  Layout         `json:"-"`
	WikiDir string         `json:"wikiDir,omitempty"`
	Problem string         `json:"problem,omitempty"`
}

// ResolveMembers resolves each wiki of ws to its current layout. A missing
// repository, an unbound repository, or a wiki that was never generated is
// reported on the member rather than failing the whole workspace.
func (d Dirs) ResolveMembers(reg WorkspaceRegistry, ws Workspace) ([]WorkspaceMember, error) {
	var out []WorkspaceMember
	for _, id := range ws.Wikis {
		w, ok := reg.WikiByID(id)
		if !ok {
			return nil, fmt.Errorf("workspace %s lists unknown wiki %s", ws.ID, id)
		}
		m := WorkspaceMember{Wiki: w}
		if _, err := os.Stat(w.Root); err != nil {
			m.Problem = "repository not found at " + w.Root
		} else if l, err := d.Resolve(w.Root); errors.Is(err, ErrUnbound) {
			m.Problem = "repository has no wiki; run `owcli bind` or `owcli init` there"
		} else if err != nil {
			m.Problem = err.Error()
		} else if fi, err := os.Stat(l.WikiRoot); err != nil || !fi.IsDir() {
			m.Problem = "wiki directory missing at " + l.WikiRoot
		} else {
			m.Layout, m.WikiDir = l, l.WikiRoot
		}
		out = append(out, m)
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
