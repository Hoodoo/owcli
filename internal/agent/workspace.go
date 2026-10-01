// Package agent runs a model in a tool-calling loop over a confined view of
// a repository and its wiki.
package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"owcli/internal/ignore"
	"owcli/internal/store"
)

// ErrDenied marks a path the agent may not access.
var ErrDenied = errors.New("access denied")

func deniedf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrDenied, fmt.Sprintf(format, args...))
}

// Workspace is the agent's view: repository files at their relative paths and
// the wiki under "openwiki/", whichever layout stores it. Reads honor
// .openwikiignore and never leave the physical roots; the wiki's hidden
// control files are invisible. Writes are limited to Writable pages.
type Workspace struct {
	Repo     string          // absolute repository root
	Wiki     string          // absolute wiki root
	Ignore   *ignore.Matcher // must already exclude the repo's own openwiki/
	Writable map[string]bool // wiki-relative pages the agent may write ("concepts/x.md")
}

// NewWorkspace builds a read-only workspace for a layout.
func NewWorkspace(l store.Layout) (*Workspace, error) {
	m, err := ignore.Load(l.RepoRoot)
	if err != nil {
		return nil, err
	}
	return &Workspace{Repo: l.RepoRoot, Wiki: l.WikiRoot, Ignore: m.Exclude(l.RepoExclusions()...), Writable: map[string]bool{}}, nil
}

// target is a resolved virtual path.
type target struct {
	virtual string // normalized virtual path ("" is the root)
	abs     string
	root    string // physical root it must stay inside
	wikiRel string // set for paths inside the wiki ("" for the wiki root itself)
	inWiki  bool
}

// resolve maps a model-supplied path to a target, enforcing confinement and
// ignore rules. isDir is a hint for directory-only ignore patterns.
func (w *Workspace) resolve(p string, isDir bool) (target, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, `\`, "/"))
	if strings.HasPrefix(p, "/") {
		p = strings.TrimLeft(p, "/")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return target{}, deniedf("path %q leaves the workspace", p)
		}
	}
	v := strings.Trim(path.Clean("/"+p), "/")
	if v == store.WikiDirName || strings.HasPrefix(v, store.WikiDirName+"/") {
		rel := strings.TrimPrefix(strings.TrimPrefix(v, store.WikiDirName), "/")
		for _, seg := range strings.Split(rel, "/") {
			if strings.HasPrefix(seg, ".") {
				return target{}, deniedf("wiki control files are not accessible: %s", v)
			}
		}
		return target{virtual: v, abs: filepath.Join(w.Wiki, filepath.FromSlash(rel)), root: w.Wiki, wikiRel: rel, inWiki: true}, nil
	}
	if v != "" && w.Ignore.Ignores(v, isDir) {
		return target{}, deniedf("%s is excluded by %s", v, ignore.FileName)
	}
	return target{virtual: v, abs: filepath.Join(w.Repo, filepath.FromSlash(v)), root: w.Repo}, nil
}

// contained checks that an existing path resolves inside its root.
func contained(t target) error {
	real, err := filepath.EvalSymlinks(t.abs)
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(t.root)
	if err != nil {
		return err
	}
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return deniedf("%s resolves outside the workspace", t.virtual)
	}
	return nil
}

const maxFileBytes = 4 << 20

// ReadFile returns a file's text.
func (w *Workspace) ReadFile(p string) (string, error) {
	t, err := w.resolve(p, false)
	if err != nil {
		return "", err
	}
	if err := contained(t); err != nil {
		return "", notFound(err, t.virtual)
	}
	fi, err := os.Stat(t.abs)
	if err != nil {
		return "", notFound(err, t.virtual)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory; use ls", t.virtual)
	}
	if fi.Size() > maxFileBytes {
		return "", fmt.Errorf("%s is too large (%d bytes)", t.virtual, fi.Size())
	}
	data, err := os.ReadFile(t.abs)
	if err != nil {
		return "", err
	}
	if isBinary(data) {
		return "", fmt.Errorf("%s is a binary file", t.virtual)
	}
	return string(data), nil
}

func notFound(err error, v string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s does not exist", v)
	}
	return err
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	for _, b := range data[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

// WriteFile creates or replaces a writable wiki page.
func (w *Workspace) WriteFile(p, content string) error {
	t, err := w.resolve(p, false)
	if err != nil {
		return err
	}
	if !t.inWiki || !w.Writable[t.wikiRel] {
		return deniedf("only the assigned page may be written (%s)", strings.Join(w.writableList(), ", "))
	}
	if err := os.MkdirAll(filepath.Dir(t.abs), 0o755); err != nil {
		return err
	}
	if err := contained(target{virtual: t.virtual, abs: filepath.Dir(t.abs), root: t.root}); err != nil {
		return err
	}
	if fi, err := os.Lstat(t.abs); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return deniedf("%s is a symbolic link", t.virtual)
	}
	return store.WriteFileAtomic(t.abs, []byte(content), 0o644)
}

func (w *Workspace) writableList() []string {
	var out []string
	for p := range w.Writable {
		out = append(out, store.WikiDirName+"/"+p)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"none"}
	}
	return out
}

// Entry is one walked file or directory.
type Entry struct {
	Path  string // virtual path
	IsDir bool
	Size  int64
}

// List returns a directory's visible entries.
func (w *Workspace) List(p string) ([]Entry, error) {
	t, err := w.resolve(p, true)
	if err != nil {
		return nil, err
	}
	if err := contained(t); err != nil {
		return nil, notFound(err, t.virtual)
	}
	if fi, err := os.Stat(t.abs); err == nil && !fi.IsDir() {
		return nil, fmt.Errorf("%s is a file; use read_file", t.virtual)
	}
	entries, err := os.ReadDir(t.abs)
	if err != nil {
		return nil, notFound(err, t.virtual)
	}
	var out []Entry
	for _, e := range entries {
		child := strings.TrimPrefix(t.virtual+"/"+e.Name(), "/")
		if e.Type()&fs.ModeSymlink != 0 || (!t.inWiki && child == store.WikiDirName) {
			continue
		}
		if _, err := w.resolve(child, e.IsDir()); err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{Path: child, IsDir: e.IsDir(), Size: info.Size()})
	}
	if t.virtual == "" {
		if fi, err := os.Stat(w.Wiki); err == nil && fi.IsDir() {
			out = append(out, Entry{Path: store.WikiDirName, IsDir: true})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	}
	return out, nil
}

// Walk visits visible regular files below p (repository and wiki), in
// lexical order, skipping ignored directories and symbolic links.
func (w *Workspace) Walk(p string, fn func(Entry) error) error {
	t, err := w.resolve(p, true)
	if err != nil {
		return err
	}
	walkRoot := func(abs, prefix string, inWiki bool) error {
		return filepath.WalkDir(abs, func(fp string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if fp == abs || d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			rel, _ := filepath.Rel(abs, fp)
			v := strings.TrimPrefix(path.Join(prefix, filepath.ToSlash(rel)), "/")
			switch {
			case inWiki && strings.HasPrefix(d.Name(), "."):
				return skip(d)
			case !inWiki && d.IsDir() && v == store.WikiDirName:
				return filepath.SkipDir // the repository's own copy is not the wiki
			case !inWiki && w.Ignore.Ignores(v, d.IsDir()):
				if d.IsDir() && w.Ignore.SkipDir(v) {
					return filepath.SkipDir
				}
				return nil
			case d.IsDir():
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			return fn(Entry{Path: v, Size: info.Size()})
		})
	}
	if t.inWiki {
		return walkRoot(t.abs, t.virtual, true)
	}
	if err := contained(t); err != nil {
		return notFound(err, t.virtual)
	}
	if err := walkRoot(t.abs, t.virtual, false); err != nil {
		return err
	}
	if t.virtual == "" {
		return walkRoot(w.Wiki, store.WikiDirName, true)
	}
	return nil
}

func skip(d fs.DirEntry) error {
	if d.IsDir() {
		return filepath.SkipDir
	}
	return nil
}
