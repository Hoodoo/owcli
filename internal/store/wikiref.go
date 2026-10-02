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
	bound  bool
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
		byRoot[root] = wikiCandidate{root: root, id: WikiID(root), bound: true}
	}
	for _, w := range reg.Wikis {
		_, bound := r.Bindings[w.Root]
		byRoot[w.Root] = wikiCandidate{root: w.Root, id: w.ID, member: true, bound: bound}
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
	w, problem, err := d.resolveCandidate(match[0])
	if err != nil {
		return ScopedWiki{}, err
	}
	if problem != "" {
		return ScopedWiki{}, workspaceErr("wiki %s: %s", w.ID, problem)
	}
	return w, nil
}

// resolveCandidate resolves a known repository to its wiki. A problem (the
// repository or its wiki is missing) is returned as text, with the identity
// still filled in; err is for failures reading state.
func (d Dirs) resolveCandidate(c wikiCandidate) (ScopedWiki, string, error) {
	w := ScopedWiki{WikiIdentity: WikiIdentity{ID: c.id, Name: filepath.Base(c.root)}}
	if _, err := os.Stat(c.root); err != nil {
		return w, "repository not found at " + c.root, nil
	}
	l, err := d.Resolve(c.root)
	if errors.Is(err, ErrUnbound) {
		return w, fmt.Sprintf("%s has no wiki; run `owcli -C %s init` or bind it", c.root, c.root), nil
	}
	if err != nil {
		return w, "", err
	}
	if fi, err := os.Stat(l.WikiRoot); err != nil || !fi.IsDir() {
		return w, "wiki directory missing at " + l.WikiRoot, nil
	}
	w.Layout = l
	return w, "", nil
}

// KnownWiki is one wiki the registries know, resolved. Problem explains why
// it cannot be inspected; Wiki.Layout is valid only when Problem is empty.
type KnownWiki struct {
	Wiki    ScopedWiki
	Root    string
	Bound   bool // in the binding registry (else only a workspace member)
	Problem string
}

// KnownWikis lists every bound repository and workspace member, in
// repository-root order, with its wiki resolved or its problem reported.
func (d Dirs) KnownWikis() ([]KnownWiki, error) {
	cands, _, err := d.candidates()
	if err != nil {
		return nil, err
	}
	out := make([]KnownWiki, 0, len(cands))
	for _, c := range cands {
		w, problem, err := d.resolveCandidate(c)
		if err != nil {
			return nil, err
		}
		out = append(out, KnownWiki{Wiki: w, Root: c.root, Bound: c.bound, Problem: problem})
	}
	return out, nil
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
