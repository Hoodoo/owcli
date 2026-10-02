package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WikiID is the stable identifier of a wiki that is not a workspace member:
// a slug of the repository name plus a hash of its root, the same ID an
// external binding uses for its directory. It needs no stored state.
func WikiID(repoRoot string) string { return ExternalID(repoRoot) }

// wikiCandidate is one known repository that a wiki reference may name.
type wikiCandidate struct {
	root   string
	id     string // workspace ID for members, else WikiID
	member bool
}

// candidates lists every repository owcli knows by registry: bindings and
// workspace members, one entry per root.
func (d Dirs) candidates() ([]wikiCandidate, WorkspaceRegistry, error) {
	r, err := d.loadRegistry()
	if err != nil {
		return nil, WorkspaceRegistry{}, err
	}
	reg, err := d.LoadWorkspaces()
	if err != nil {
		return nil, WorkspaceRegistry{}, err
	}
	byRoot := map[string]wikiCandidate{}
	for root := range r.Bindings {
		byRoot[root] = wikiCandidate{root: root, id: WikiID(root)}
	}
	for _, w := range reg.Wikis {
		byRoot[w.Root] = wikiCandidate{root: w.Root, id: w.ID, member: true}
	}
	out := make([]wikiCandidate, 0, len(byRoot))
	for _, c := range byRoot {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].root < out[j].root })
	return out, reg, nil
}

// ResolveWikiRef finds a registered wiki from anywhere by reference: a
// workspace wiki ID, a WikiID, or a repository name when exactly one known
// repository has it. Only bound repositories and workspace members are
// known; reach any other repository with its path.
func (d Dirs) ResolveWikiRef(ref string) (ScopedWiki, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ScopedWiki{}, workspaceErr("give a wiki ID or repository name")
	}
	cands, _, err := d.candidates()
	if err != nil {
		return ScopedWiki{}, err
	}
	var match []wikiCandidate
	name := slugID(ref, "")
	for _, c := range cands {
		if c.id == ref || WikiID(c.root) == ref {
			match = []wikiCandidate{c}
			break
		}
		if name != "" && slugID(filepath.Base(c.root), "") == name {
			match = append(match, c)
		}
	}
	switch len(match) {
	case 0:
		return ScopedWiki{}, workspaceErr("no known wiki %q; `owcli bindings` and `owcli workspace list` show IDs", ref)
	case 1:
	default:
		var ids []string
		for _, c := range match {
			ids = append(ids, c.id+" ("+c.root+")")
		}
		return ScopedWiki{}, workspaceErr("wiki %q is ambiguous; use one of: %s", ref, strings.Join(ids, ", "))
	}
	c := match[0]
	if _, err := os.Stat(c.root); err != nil {
		return ScopedWiki{}, workspaceErr("wiki %s: repository not found at %s", c.id, c.root)
	}
	l, err := d.Resolve(c.root)
	if errors.Is(err, ErrUnbound) {
		return ScopedWiki{}, workspaceErr("wiki %s: %s has no wiki; run `owcli -C %s init` or bind it", c.id, c.root, c.root)
	}
	if err != nil {
		return ScopedWiki{}, err
	}
	if fi, err := os.Stat(l.WikiRoot); err != nil || !fi.IsDir() {
		return ScopedWiki{}, workspaceErr("wiki %s: wiki directory missing at %s", c.id, l.WikiRoot)
	}
	return ScopedWiki{WikiIdentity: WikiIdentity{ID: c.id, Name: filepath.Base(c.root)}, Layout: l}, nil
}

// ResolveLayoutRef resolves the wiki an operator command (status, check)
// targets: the repository containing dir when ref is empty, otherwise ref
// from anywhere.
func (d Dirs) ResolveLayoutRef(dir, ref string) (Layout, error) {
	if ref == "" {
		return d.Resolve(dir)
	}
	w, err := d.ResolveWikiRef(ref)
	if err != nil {
		return Layout{}, fmt.Errorf("--wiki: %w", err)
	}
	return w.Layout, nil
}
