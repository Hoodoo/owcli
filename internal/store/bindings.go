package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrUnbound reports a repository with neither a registry entry nor an
// in-repo wiki.
var ErrUnbound = errors.New("repository is not bound; run `owcli bind`")

const bindingsSchemaVersion = 1

// Dirs locates owcli's own state outside any repository.
type Dirs struct {
	Config string // holds bindings.json
	Data   string // holds wikis/<id>/openwiki for external layouts
}

// DefaultDirs resolves $XDG_CONFIG_HOME/owcli and $XDG_DATA_HOME/owcli.
func DefaultDirs() (Dirs, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Dirs{}, err
	}
	xdg := func(env, fallback string) string {
		if v := os.Getenv(env); v != "" {
			return filepath.Join(v, "owcli")
		}
		return filepath.Join(home, fallback, "owcli")
	}
	return Dirs{
		Config: xdg("XDG_CONFIG_HOME", ".config"),
		Data:   xdg("XDG_DATA_HOME", filepath.Join(".local", "share")),
	}, nil
}

// Binding is one registry entry.
type Binding struct {
	Kind    Kind      `json:"kind"`
	ID      string    `json:"id,omitempty"`      // external wiki directory name
	WikiDir string    `json:"wikiDir,omitempty"` // custom external location (absolute)
	BoundAt time.Time `json:"boundAt"`
}

type registry struct {
	SchemaVersion int                `json:"schemaVersion"`
	Bindings      map[string]Binding `json:"bindings"` // keyed by repository root
}

// BindingInfo is a registry entry enriched with filesystem and run metadata.
type BindingInfo struct {
	ID         string      `json:"id"` // WikiID, usable as --wiki from anywhere
	RepoRoot   string      `json:"repoRoot"`
	Kind       Kind        `json:"kind"`
	WikiDir    string      `json:"wikiDir"`
	RepoExists bool        `json:"repoExists"`
	Custom     bool        `json:"custom"`
	LastUpdate *LastUpdate `json:"lastUpdate,omitempty"`
}

// BindingsInventory describes every registered repository and unreferenced
// directory in owcli's managed external-wiki directory.
type BindingsInventory struct {
	Bindings []BindingInfo `json:"bindings"`
	Orphans  []string      `json:"orphans"`
}

func (d Dirs) registryPath() string { return filepath.Join(d.Config, "bindings.json") }

func (d Dirs) externalWikiDir(id string) string { return filepath.Join(d.Data, "wikis", id) }

func (d Dirs) loadRegistry() (registry, error) {
	path := d.registryPath()
	var r registry
	found, err := ReadJSON(path, &r)
	if err != nil {
		return registry{}, err
	}
	if !found {
		return registry{SchemaVersion: bindingsSchemaVersion, Bindings: map[string]Binding{}}, nil
	}
	if r.SchemaVersion != bindingsSchemaVersion {
		return registry{}, invalid(path, "unsupported schemaVersion %d", r.SchemaVersion)
	}
	if r.Bindings == nil {
		r.Bindings = map[string]Binding{}
	}
	for root, b := range r.Bindings {
		switch {
		case !filepath.IsAbs(root):
			return registry{}, invalid(path, "binding key %q is not absolute", root)
		case b.Kind == External && b.ID == "":
			return registry{}, invalid(path, "external binding %q has no id", root)
		case b.Kind != InRepo && b.Kind != External:
			return registry{}, invalid(path, "binding %q has unknown kind %q", root, b.Kind)
		}
	}
	return r, nil
}

func (d Dirs) layoutFor(root string, b Binding) Layout {
	if b.Kind == External {
		home := d.externalWikiDir(b.ID)
		if b.WikiDir != "" {
			home = b.WikiDir
		}
		return Layout{Kind: External, RepoRoot: root, Home: home, WikiRoot: filepath.Join(home, WikiDirName), CustomHome: b.WikiDir != ""}
	}
	return Layout{Kind: InRepo, RepoRoot: root, Home: root, WikiRoot: filepath.Join(root, WikiDirName)}
}

// ListBindings returns registered bindings and managed wiki homes that no
// binding references. User-chosen wiki directories are never classified as
// orphans because owcli does not own their parent directory.
func (d Dirs) ListBindings() (BindingsInventory, error) {
	r, err := d.loadRegistry()
	if err != nil {
		return BindingsInventory{}, err
	}
	inv := BindingsInventory{}
	referenced := map[string]bool{}
	for root, b := range r.Bindings {
		l := d.layoutFor(root, b)
		_, statErr := os.Stat(root)
		info := BindingInfo{ID: WikiID(root), RepoRoot: root, Kind: b.Kind, WikiDir: l.WikiRoot, RepoExists: statErr == nil, Custom: l.CustomHome}
		if info.LastUpdate, err = l.LoadLastUpdate(); err != nil {
			return BindingsInventory{}, err
		}
		inv.Bindings = append(inv.Bindings, info)
		if b.Kind == External {
			referenced[l.Home] = true
		}
	}
	sort.Slice(inv.Bindings, func(i, j int) bool { return inv.Bindings[i].RepoRoot < inv.Bindings[j].RepoRoot })
	root := filepath.Join(d.Data, "wikis")
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return BindingsInventory{}, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			home := filepath.Join(root, entry.Name())
			if !referenced[home] {
				inv.Orphans = append(inv.Orphans, home)
			}
		}
	}
	return inv, nil
}

// customWikiDir validates a user-chosen external wiki location: absolute,
// and not inside the repository it documents.
func customWikiDir(dir, repoRoot string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if real == repoRoot || strings.HasPrefix(real, repoRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("--wiki-dir %s is inside the repository %s; an external wiki must live outside it", dir, repoRoot)
	}
	return real, nil
}

// Resolve finds the layout for the repository containing dir: an explicit
// binding wins; otherwise an existing <repo>/openwiki directory is an implicit
// in-repo binding; otherwise ErrUnbound.
func (d Dirs) Resolve(dir string) (Layout, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return Layout{}, err
	}
	r, err := d.loadRegistry()
	if err != nil {
		return Layout{}, err
	}
	if b, ok := r.Bindings[root]; ok {
		return d.layoutFor(root, b), nil
	}
	if fi, err := os.Stat(filepath.Join(root, WikiDirName)); err == nil && fi.IsDir() {
		return d.layoutFor(root, Binding{Kind: InRepo}), nil
	}
	return Layout{}, fmt.Errorf("%s: %w", root, ErrUnbound)
}

// Bind registers the repository containing dir. Binding again with the same
// kind and location is a no-op; switching requires Unbind first so no wiki
// is silently orphaned. An external bind creates the wiki directory (under
// Data, or in wikiDir when given) and writes nothing into the repository.
func (d Dirs) Bind(dir string, kind Kind, wikiDir string, now time.Time) (Layout, error) {
	if kind != InRepo && kind != External {
		return Layout{}, fmt.Errorf("unknown binding kind %q", kind)
	}
	if wikiDir != "" && kind != External {
		return Layout{}, errors.New("a wiki directory can only be chosen for external bindings")
	}
	root, err := RepoRoot(dir)
	if err != nil {
		return Layout{}, err
	}
	r, err := d.loadRegistry()
	if err != nil {
		return Layout{}, err
	}
	custom := ""
	if wikiDir != "" {
		if custom, err = customWikiDir(wikiDir, root); err != nil {
			return Layout{}, err
		}
	}
	if b, ok := r.Bindings[root]; ok {
		if b.Kind != kind || (custom != "" && b.WikiDir != custom) {
			return Layout{}, fmt.Errorf("%s is already bound %s at %s; unbind it first", root, b.Kind, d.layoutFor(root, b).WikiRoot)
		}
		return d.layoutFor(root, b), nil
	}
	// Reattaching an existing wiki home after a clone was moved transfers a
	// stale registry entry. Never steal a wiki from a repository that exists.
	if kind == External && custom != "" {
		for oldRoot, oldBinding := range r.Bindings {
			if d.layoutFor(oldRoot, oldBinding).Home != custom {
				continue
			}
			if _, err := os.Stat(oldRoot); err == nil || !os.IsNotExist(err) {
				return Layout{}, fmt.Errorf("wiki directory %s is already bound to %s", custom, oldRoot)
			}
			delete(r.Bindings, oldRoot)
			break
		}
	}
	b := Binding{Kind: kind, BoundAt: now.UTC(), WikiDir: custom}
	if kind == External {
		b.ID = ExternalID(root)
	}
	l := d.layoutFor(root, b)
	if kind == External {
		if err := os.MkdirAll(l.WikiRoot, 0o755); err != nil {
			return Layout{}, err
		}
	}
	r.Bindings[root] = b
	if err := WriteJSONAtomic(d.registryPath(), r); err != nil {
		return Layout{}, err
	}
	return l, nil
}

// Unbind forgets the binding for the repository containing dir and returns
// the layout it had. With purge, an external wiki is deleted; an in-repo wiki
// is never deleted, since it belongs to the repository.
func (d Dirs) Unbind(dir string, purge bool) (Layout, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return Layout{}, err
	}
	r, err := d.loadRegistry()
	if err != nil {
		return Layout{}, err
	}
	b, ok := r.Bindings[root]
	if !ok {
		return Layout{}, fmt.Errorf("%s: %w", root, ErrUnbound)
	}
	if purge && b.Kind == InRepo {
		return Layout{}, fmt.Errorf("refusing to purge in-repo wiki %s; delete it with git if intended", filepath.Join(root, WikiDirName))
	}
	if purge && b.WikiDir != "" {
		return Layout{}, fmt.Errorf("refusing to purge %s: it is a directory you chose; delete it yourself if intended", b.WikiDir)
	}
	l := d.layoutFor(root, b)
	delete(r.Bindings, root)
	if err := WriteJSONAtomic(d.registryPath(), r); err != nil {
		return Layout{}, err
	}
	if purge {
		if err := os.RemoveAll(d.externalWikiDir(b.ID)); err != nil {
			return l, err
		}
	}
	return l, nil
}
