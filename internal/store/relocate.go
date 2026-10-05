package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PathChange is one stored absolute path that Relocate rewrites.
type PathChange struct {
	Registry string `json:"registry"` // "bindings" or "workspaces"
	Field    string `json:"field"`    // "repoRoot", "wikiDir", or "root"
	From     string `json:"from"`
	To       string `json:"to"`
	Exists   bool   `json:"exists"` // whether To exists on disk
}

// underPrefix reports whether path is prefix or inside it, and returns path
// with prefix replaced by repl.
func underPrefix(path, prefix, repl string) (string, bool) {
	if path == prefix {
		return repl, true
	}
	if rest, ok := strings.CutPrefix(path, prefix+string(filepath.Separator)); ok {
		return filepath.Join(repl, rest), true
	}
	return "", false
}

// relocatePrefixes makes both prefixes absolute and clean. The new prefix is
// resolved through symlinks when it exists, because registry keys are
// canonical repository roots; the old one usually no longer exists.
func relocatePrefixes(oldPrefix, newPrefix string) (string, string, error) {
	from, err := filepath.Abs(oldPrefix)
	if err != nil {
		return "", "", err
	}
	to, err := filepath.Abs(newPrefix)
	if err != nil {
		return "", "", err
	}
	if resolved, err := filepath.EvalSymlinks(to); err == nil {
		to = resolved
	}
	if from == to {
		return "", "", fmt.Errorf("old and new path are both %s", from)
	}
	return from, to, nil
}

// Relocate rewrites every stored path under oldPrefix to the same path under
// newPrefix: binding keys and custom external wiki directories in
// bindings.json, and wiki roots in workspaces.json. It is for repositories
// (or a whole home directory) that moved. Wiki and workspace IDs, active
// selections, and managed external wiki directories are unchanged. Nothing
// is written when dryRun is set, when nothing matches, or when a rewritten
// path would collide with one already registered.
func (d Dirs) Relocate(oldPrefix, newPrefix string, dryRun bool) ([]PathChange, error) {
	from, to, err := relocatePrefixes(oldPrefix, newPrefix)
	if err != nil {
		return nil, err
	}
	reg, err := d.loadRegistry()
	if err != nil {
		return nil, err
	}
	ws, err := d.LoadWorkspaces()
	if err != nil {
		return nil, err
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	var changes []PathChange

	roots := make([]string, 0, len(reg.Bindings))
	for root := range reg.Bindings {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	next := make(map[string]Binding, len(reg.Bindings))
	for _, root := range roots {
		b := reg.Bindings[root]
		key := root
		if moved, ok := underPrefix(root, from, to); ok {
			key = moved
			changes = append(changes, PathChange{"bindings", "repoRoot", root, moved, exists(moved)})
		}
		if b.WikiDir != "" {
			if moved, ok := underPrefix(b.WikiDir, from, to); ok {
				changes = append(changes, PathChange{"bindings", "wikiDir", b.WikiDir, moved, exists(moved)})
				b.WikiDir = moved
			}
		}
		if _, taken := next[key]; taken {
			return nil, fmt.Errorf("%s would be bound twice; unbind one of them first", key)
		}
		next[key] = b
	}
	for i, w := range ws.Wikis {
		if moved, ok := underPrefix(w.Root, from, to); ok {
			changes = append(changes, PathChange{"workspaces", "root", w.Root, moved, exists(moved)})
			ws.Wikis[i].Root = moved
		}
	}
	if err := ws.validate(); err != nil {
		return nil, fmt.Errorf("relocating would make workspaces.json invalid: %w", err)
	}
	if dryRun || len(changes) == 0 {
		return changes, nil
	}
	touched := map[string]bool{}
	for _, c := range changes {
		touched[c.Registry] = true
	}
	if touched["bindings"] {
		reg.Bindings = next
		if err := WriteJSONAtomic(d.registryPath(), reg); err != nil {
			return nil, err
		}
	}
	if touched["workspaces"] {
		if err := WriteJSONAtomic(d.workspacesPath(), ws); err != nil {
			return nil, fmt.Errorf("bindings.json was relocated but workspaces.json was not: %w", err)
		}
	}
	return changes, nil
}
