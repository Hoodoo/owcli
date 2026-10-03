package claims

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Hoodoo/owcli/internal/store"
)

// Store persists pages' Claim sidecars and reads and writes page Markdown for
// one wiki. Every path is checked to stay physically inside the wiki root:
// symbolic links and filesystem aliases are refused.
type Store struct {
	wikiRoot string
	realRoot string // resolved lazily
}

// NewStore opens the Claims store of a layout.
func NewStore(l store.Layout) *Store { return &Store{wikiRoot: l.WikiRoot} }

func (s *Store) sidecarPath(page string) string {
	return filepath.Join(s.wikiRoot, ClaimsDirName, filepath.FromSlash(sidecarRel(page)))
}

func (s *Store) pagePath(page string) string {
	return filepath.Join(s.wikiRoot, filepath.FromSlash(pageRel(page)))
}

func (s *Store) display(abs string) string {
	rel, err := filepath.Rel(s.wikiRoot, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

func (s *Store) root() (string, error) {
	if s.realRoot == "" {
		real, err := filepath.EvalSymlinks(s.wikiRoot)
		if err != nil {
			return "", fmt.Errorf("%w: resolve wiki root %s: %v", ErrSecurity, s.wikiRoot, err)
		}
		s.realRoot = real
	}
	return s.realRoot, nil
}

// physical checks that an existing path resolves exactly to its location
// below the real wiki root.
func (s *Store) physical(abs string) (string, error) {
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", persistencef("resolve %s: %v", s.display(abs), err)
	}
	root, err := s.root()
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(s.wikiRoot, abs)
	if err != nil || real != filepath.Join(root, rel) {
		return "", fmt.Errorf("%w: path traverses a symbolic link or filesystem alias: %s", ErrSecurity, s.display(abs))
	}
	return real, nil
}

// existingFile returns the physical path of a regular file, "" if missing.
func (s *Store) existingFile(abs string) (string, error) {
	fi, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", persistencef("inspect %s: %v", s.display(abs), err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: path cannot be a symbolic link: %s", ErrSecurity, s.display(abs))
	}
	if !fi.Mode().IsRegular() {
		return "", persistencef("not a regular file: %s", s.display(abs))
	}
	return s.physical(abs)
}

// existingDir returns the physical path of a directory, "" if missing.
func (s *Store) existingDir(abs string) (string, error) {
	fi, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", persistencef("inspect %s: %v", s.display(abs), err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: path cannot be a symbolic link: %s", ErrSecurity, s.display(abs))
	}
	if !fi.IsDir() {
		return "", persistencef("not a directory: %s", s.display(abs))
	}
	return s.physical(abs)
}

// containedDir creates dir below the wiki root, refusing symlinked ancestors.
func (s *Store) containedDir(dir string) (string, error) {
	rel, err := filepath.Rel(s.wikiRoot, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: directory escapes the wiki: %s", ErrSecurity, dir)
	}
	cur := s.wikiRoot
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == "" || seg == "." {
			continue
		}
		cur = filepath.Join(cur, seg)
		phys, err := s.existingDir(cur)
		if err != nil {
			return "", err
		}
		if phys == "" {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", persistencef("create %s: %v", s.display(dir), err)
	}
	phys, err := s.existingDir(dir)
	if err == nil && phys == "" {
		err = persistencef("directory disappeared: %s", s.display(dir))
	}
	return phys, err
}

// walk lists regular files below dir (relative, slash-separated, sorted),
// skipping symbolic links and, unless includeClaims, the .claims directory.
func (s *Store) walk(dir string, includeClaims bool) ([]string, error) {
	phys, err := s.existingDir(dir)
	if err != nil || phys == "" {
		return nil, err
	}
	var out []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return persistencef("enumerate %s: %v", p, err)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if p != dir && !includeClaims && strings.EqualFold(d.Name(), ClaimsDirName) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// DiscoverPages lists the grounded pages present in the wiki.
func (s *Store) DiscoverPages() ([]string, error) {
	files, err := s.walk(s.wikiRoot, false)
	if err != nil {
		return nil, err
	}
	var pages []string
	for _, f := range files {
		if p := wikiPrefix + f; IsGroundedPage(p) {
			pages = append(pages, p)
		}
	}
	return pages, nil
}

// DiscoverSidecarPages lists the pages that have a sidecar.
func (s *Store) DiscoverSidecarPages() ([]string, error) {
	files, err := s.walk(filepath.Join(s.wikiRoot, ClaimsDirName), true)
	if err != nil {
		return nil, err
	}
	var pages []string
	for _, f := range files {
		if !strings.HasSuffix(f, ".json") {
			continue
		}
		if p := wikiPrefix + strings.TrimSuffix(f, ".json") + ".md"; IsGroundedPage(p) {
			pages = append(pages, p)
		}
	}
	sort.Strings(pages)
	return pages, nil
}

// Load reads one page's sidecar; nil when it has none.
func (s *Store) Load(page string) (*PageClaims, error) {
	sidecar := s.sidecarPath(page)
	// Refuse a symlinked or aliased .claims directory even when the sidecar
	// itself is absent, so a redirected directory is never read as "empty".
	if _, err := s.existingDir(filepath.Join(s.wikiRoot, ClaimsDirName)); err != nil {
		return nil, err
	}
	phys, err := s.existingFile(sidecar)
	if err != nil || phys == "" {
		return nil, err
	}
	data, err := os.ReadFile(phys)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, persistencef("read %s: %v", s.display(sidecar), err)
	}
	var pc PageClaims
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pc); err != nil {
		return nil, persistencef("invalid sidecar %s: %v", s.display(sidecar), err)
	}
	if err := validatePageClaims(pc); err != nil {
		return nil, persistencef("invalid sidecar %s: %v", s.display(sidecar), err)
	}
	return &pc, nil
}

// LoadAll reads the sidecars of the given pages, keyed by page.
func (s *Store) LoadAll(pages []string) (map[string]*PageClaims, error) {
	out := map[string]*PageClaims{}
	for _, p := range pages {
		pc, err := s.Load(p)
		if err != nil {
			return nil, err
		}
		if pc != nil {
			out[p] = pc
		}
	}
	return out, nil
}

// Write validates and atomically persists a page's sidecar.
func (s *Store) Write(page string, pc PageClaims) error {
	if err := validatePageClaims(pc); err != nil {
		return persistencef("claims for %s: %v", page, err)
	}
	sidecar := s.sidecarPath(page)
	dir, err := s.containedDir(filepath.Dir(sidecar))
	if err != nil {
		return err
	}
	data, err := store.MarshalJSON(pc)
	if err != nil {
		return persistencef("encode claims for %s: %v", page, err)
	}
	if err := store.WriteFileAtomic(filepath.Join(dir, filepath.Base(sidecar)), data, 0o644); err != nil {
		return persistencef("persist %s: %v", s.display(sidecar), err)
	}
	return nil
}

// Delete removes a page's sidecar if present.
func (s *Store) Delete(page string) error {
	sidecar := s.sidecarPath(page)
	dir, err := s.existingDir(filepath.Dir(sidecar))
	if err != nil || dir == "" {
		return err
	}
	if err := store.RemoveIfExists(filepath.Join(dir, filepath.Base(sidecar))); err != nil {
		return persistencef("remove claims for %s: %v", page, err)
	}
	return nil
}

// ReadMarkdown returns a page's content.
func (s *Store) ReadMarkdown(page string) (string, error) {
	phys, err := s.existingFile(s.pagePath(page))
	if err != nil {
		return "", err
	}
	if phys == "" {
		return "", fmt.Errorf("%w: %s", ErrPageMissing, page)
	}
	data, err := os.ReadFile(phys)
	if err != nil {
		return "", persistencef("read %s: %v", page, err)
	}
	return string(data), nil
}

// WriteMarkdown replaces an existing page's content.
func (s *Store) WriteMarkdown(page, content string) error {
	phys, err := s.existingFile(s.pagePath(page))
	if err != nil {
		return err
	}
	if phys == "" {
		return fmt.Errorf("%w: %s", ErrPageMissing, page)
	}
	if err := store.WriteFileAtomic(phys, []byte(content), 0o644); err != nil {
		return persistencef("write %s: %v", page, err)
	}
	return nil
}

// HashPage returns "sha256:<hex>" of a page's exact bytes.
func (s *Store) HashPage(page string) (string, error) {
	content, err := s.ReadMarkdown(page)
	if err != nil {
		return "", err
	}
	return HashContent(content), nil
}

// HashContent returns the page version of content.
func HashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

var pageVersionPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func canonical(v, label string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s cannot be empty", label)
	}
	if v != strings.TrimSpace(v) {
		return fmt.Errorf("%s cannot contain surrounding whitespace", label)
	}
	return nil
}

func validatePageClaims(pc PageClaims) error {
	if pc.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %d", pc.SchemaVersion)
	}
	if !pageVersionPattern.MatchString(pc.PageVersion) {
		return fmt.Errorf("invalid pageVersion %q", pc.PageVersion)
	}
	if pc.Claims == nil {
		return errors.New("claims must be a list")
	}
	if pc.Verification != nil {
		if err := canonical(pc.Verification.By, "verification.by"); err != nil {
			return err
		}
		if err := canonical(pc.Verification.At, "verification.at"); err != nil {
			return err
		}
	}
	return validateClaims(pc.Claims)
}

// validateClaims checks ids, statements, and evidence of a Claim set.
func validateClaims(claims []Claim) error {
	ids := map[string]bool{}
	for _, c := range claims {
		if err := canonical(c.ID, "claim id"); err != nil {
			return err
		}
		if ids[c.ID] {
			return fmt.Errorf("duplicate claim id %s", c.ID)
		}
		ids[c.ID] = true
		if err := canonical(c.Statement, "claim "+c.ID+" statement"); err != nil {
			return err
		}
		if len(c.Evidence) == 0 {
			return fmt.Errorf("claim %s requires at least one evidence resource", c.ID)
		}
		seen := map[string]bool{}
		for _, e := range c.Evidence {
			if err := canonical(e.Resource, "claim "+c.ID+" evidence resource"); err != nil {
				return err
			}
			if err := canonical(e.Version, "claim "+c.ID+" evidence version"); err != nil {
				return err
			}
			if seen[e.Resource] {
				return fmt.Errorf("claim %s repeats evidence %s", c.ID, e.Resource)
			}
			seen[e.Resource] = true
		}
	}
	return nil
}
