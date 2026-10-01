package okf

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"owcli/internal/store"
)

// reservedNames are structural files that are never concepts.
var reservedNames = map[string]bool{"index.md": true, "log.md": true, "INSTRUCTIONS.md": true}

// Wiki is a wiki directory plus the repository it documents. Pages are
// addressed by canonical ids ("/openwiki/concepts/x.md") so the same id works
// whether the wiki lives inside the repository or outside it.
type Wiki struct {
	Root string // absolute wiki root (the directory named openwiki)
	Repo string // absolute repository root; used to resolve links to source
}

// ForLayout returns the Wiki of a store layout.
func ForLayout(l store.Layout) Wiki { return Wiki{Root: l.WikiRoot, Repo: l.RepoRoot} }

func (w Wiki) abs(id string) (string, error) {
	rel, ok := store.PageRel(id)
	if !ok {
		return "", fmt.Errorf("invalid wiki path %q", id)
	}
	return filepath.Join(w.Root, filepath.FromSlash(rel)), nil
}

// Read returns a wiki file's content.
func (w Wiki) Read(id string) (string, error) {
	p, err := w.abs(id)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	return string(data), err
}

// Write atomically replaces a wiki file, creating parent directories.
func (w Wiki) Write(id, content string) error {
	p, err := w.abs(id)
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(p, []byte(content), 0o644)
}

// dir is one visible wiki directory and its visible entries.
type dir struct {
	id      string // "/openwiki" or "/openwiki/sub"
	files   []string
	subdirs []string
}

// dirs lists every visible directory, deepest first. Hidden entries and
// symbolic links are skipped. A missing wiki root yields nothing.
func (w Wiki) dirs() ([]dir, error) {
	var out []dir
	var walk func(abs, id string) error
	walk = func(abs, id string) error {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		d := dir{id: id}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") || e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			if e.IsDir() {
				d.subdirs = append(d.subdirs, name)
				if err := walk(filepath.Join(abs, name), id+"/"+name); err != nil {
					return err
				}
			} else if e.Type().IsRegular() {
				d.files = append(d.files, name)
			}
		}
		out = append(out, d)
		return nil
	}
	err := walk(w.Root, "/"+store.WikiDirName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return out, err
}

func isConceptName(name string) bool {
	return strings.EqualFold(path.Ext(name), ".md") && !reservedNames[name]
}

// ConceptPages lists every concept page id, sorted.
func (w Wiki) ConceptPages() ([]string, error) {
	dirs, err := w.dirs()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range dirs {
		for _, f := range d.files {
			if isConceptName(f) {
				out = append(out, d.id+"/"+f)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// Migrate repairs every concept page's front matter in place so authoring
// starts from a conformant wiki.
func (w Wiki) Migrate(conceptType string) error {
	pages, err := w.ConceptPages()
	if err != nil {
		return err
	}
	for _, p := range pages {
		if _, err := w.normalize(p, conceptType); err != nil {
			return err
		}
	}
	return nil
}

// normalize repairs one page and returns its (possibly new) content.
func (w Wiki) normalize(id, conceptType string) (string, error) {
	content, err := w.Read(id)
	if err != nil {
		return "", err
	}
	repaired, changed := Repair(content, id, conceptType)
	if changed {
		if err := w.Write(id, repaired); err != nil {
			return "", err
		}
	}
	return repaired, nil
}
