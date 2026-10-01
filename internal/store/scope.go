package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
)

// Scope statuses, as upstream reports them.
const (
	ScopeReady             = "ready"
	ScopeWorkspaceRequired = "workspace_required"
)

// WikiIdentity names a wiki to retrieval clients. ID is empty for a wiki
// that belongs to no workspace.
type WikiIdentity struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// WorkspaceSummary is the compact identity of a workspace.
type WorkspaceSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	WikiCount int    `json:"wikiCount"`
}

// ScopedWiki is one searchable wiki with its resolved layout.
type ScopedWiki struct {
	WikiIdentity
	Layout Layout `json:"-"`
}

// SearchScope is the set of wikis a search from one repository covers. With
// status workspace_required, Choices lists the workspaces to pick from and
// Wikis is empty. Skipped lists workspace members that cannot be searched.
type SearchScope struct {
	Status    string             `json:"status"`
	Current   WikiIdentity       `json:"current"`
	Workspace *WorkspaceSummary  `json:"workspace,omitempty"`
	Wikis     []ScopedWiki       `json:"-"`
	Skipped   []WorkspaceMember  `json:"skipped,omitempty"`
	Choices   []WorkspaceSummary `json:"workspaces,omitempty"`
}

func summarize(ws Workspace) WorkspaceSummary {
	return WorkspaceSummary{ID: ws.ID, Name: ws.Name, WikiCount: len(ws.Wikis)}
}

// scopeContext is the current repository as the registry sees it.
type scopeContext struct {
	reg        WorkspaceRegistry
	root       string
	layout     Layout
	hasWiki    bool
	member     RegisteredWiki
	isMember   bool
	containing []Workspace
}

// current resolves the repository containing dir. A repository that belongs
// to a workspace may have no wiki of its own (it can still search the
// workspace); any other repository must have one.
func (d Dirs) current(dir string) (scopeContext, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return scopeContext{}, err
	}
	reg, err := d.LoadWorkspaces()
	if err != nil {
		return scopeContext{}, err
	}
	c := scopeContext{reg: reg, root: root}
	c.member, c.isMember = reg.WikiByRoot(root)
	if c.isMember {
		for _, ws := range reg.Workspaces {
			if contains(ws.Wikis, c.member.ID) {
				c.containing = append(c.containing, ws)
			}
		}
		sort.Slice(c.containing, func(i, j int) bool { return c.containing[i].ID < c.containing[j].ID })
	}
	l, err := d.Resolve(root)
	switch {
	case err == nil:
		c.layout, c.hasWiki = l, true
	case errors.Is(err, ErrUnbound) && len(c.containing) > 0:
	default:
		return scopeContext{}, err
	}
	return c, nil
}

func (c scopeContext) identity() WikiIdentity {
	if c.isMember {
		return WikiIdentity{ID: c.member.ID, Name: c.member.Name}
	}
	return WikiIdentity{Name: filepath.Base(c.root)}
}

// containingWorkspace finds ref among the workspaces containing the current
// repository.
func (c scopeContext) containingWorkspace(ref string) (Workspace, error) {
	ws, err := c.reg.FindWorkspace(ref)
	if err != nil {
		return Workspace{}, err
	}
	for _, w := range c.containing {
		if w.ID == ws.ID {
			return ws, nil
		}
	}
	return Workspace{}, workspaceErr("%s is not a member of workspace %s", c.root, ws.Name)
}

// ResolveSearchScope applies the workspace selection rules for a search
// starting in dir: an explicit workspace (ID or name) must contain the
// repository; otherwise a repository in no workspace searches its own wiki,
// one in a single workspace searches it, one in several uses its active
// workspace, and with none active the result is workspace_required.
func (d Dirs) ResolveSearchScope(dir, requested string) (SearchScope, error) {
	c, err := d.current(dir)
	if err != nil {
		return SearchScope{}, err
	}
	scope := SearchScope{Status: ScopeReady, Current: c.identity()}
	var ws Workspace
	switch {
	case requested != "":
		if ws, err = c.containingWorkspace(requested); err != nil {
			return SearchScope{}, err
		}
	case len(c.containing) == 0:
		scope.Wikis = []ScopedWiki{{WikiIdentity: scope.Current, Layout: c.layout}}
		return scope, nil
	case len(c.containing) == 1:
		ws = c.containing[0]
	default:
		active, _ := c.reg.ActiveFor(c.member.ID)
		found := false
		for _, w := range c.containing {
			if w.ID == active {
				ws, found = w, true
			}
		}
		if !found {
			scope.Status = ScopeWorkspaceRequired
			for _, w := range c.containing {
				scope.Choices = append(scope.Choices, summarize(w))
			}
			return scope, nil
		}
	}
	sum := summarize(ws)
	scope.Workspace = &sum
	members, err := d.ResolveMembers(c.reg, ws)
	if err != nil {
		return SearchScope{}, err
	}
	for _, m := range members {
		if m.Problem != "" {
			scope.Skipped = append(scope.Skipped, m)
			continue
		}
		scope.Wikis = append(scope.Wikis, ScopedWiki{WikiIdentity: WikiIdentity{ID: m.Wiki.ID, Name: m.Wiki.Name}, Layout: m.Layout})
	}
	return scope, nil
}

// ResolveReadableWiki returns the wiki a read from dir may open: the current
// repository's own wiki when wikiID is empty or names it, otherwise a wiki
// that shares a workspace with the current repository.
func (d Dirs) ResolveReadableWiki(dir, wikiID string) (ScopedWiki, error) {
	c, err := d.current(dir)
	if err != nil {
		return ScopedWiki{}, err
	}
	if wikiID == "" || (c.isMember && wikiID == c.member.ID) {
		if !c.hasWiki {
			return ScopedWiki{}, fmt.Errorf("%s: %w", c.root, ErrUnbound)
		}
		return ScopedWiki{WikiIdentity: c.identity(), Layout: c.layout}, nil
	}
	for _, ws := range c.containing {
		if !contains(ws.Wikis, wikiID) {
			continue
		}
		members, err := d.ResolveMembers(c.reg, Workspace{ID: ws.ID, Wikis: []string{wikiID}})
		if err != nil {
			return ScopedWiki{}, err
		}
		m := members[0]
		if m.Problem != "" {
			return ScopedWiki{}, workspaceErr("wiki %s cannot be read: %s", wikiID, m.Problem)
		}
		return ScopedWiki{WikiIdentity: WikiIdentity{ID: m.Wiki.ID, Name: m.Wiki.Name}, Layout: m.Layout}, nil
	}
	return ScopedWiki{}, workspaceErr("wiki %q shares no workspace with %s; list readable wikis with `owcli workspace wikis <workspace>`", wikiID, c.root)
}
