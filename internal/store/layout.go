// Package store owns where a wiki and its control state live on disk, the
// registry of bound repositories, and atomic persistence of JSON state.
//
// Every other package receives a resolved Layout and asks it for paths; none
// of them knows whether the wiki sits inside the repository or outside it.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// WikiDirName is the wiki directory name. Pages are identified as
// "/openwiki/<path>" in both layouts, matching upstream manifests.
const WikiDirName = "openwiki"

// Kind is where a wiki is stored.
type Kind string

const (
	// InRepo stores the wiki at <repo>/openwiki.
	InRepo Kind = "in-repo"
	// External stores the wiki under owcli's data directory and writes
	// nothing into the repository.
	External Kind = "external"
)

// Layout is a resolved binding: one repository and the location of its wiki.
type Layout struct {
	Kind     Kind
	RepoRoot string // absolute, symlink-free repository top level
	WikiRoot string // absolute directory holding the pages
}

// RepoExclusions lists repository-relative directories that are generated
// wiki output rather than source. Evidence, agent reads, and fingerprints must
// skip them. A repository explored externally may still carry an upstream
// openwiki/ directory; it is excluded too.
func (l Layout) RepoExclusions() []string { return []string{WikiDirName} }

// ClaimsDir holds Claims sidecars.
func (l Layout) ClaimsDir() string { return filepath.Join(l.WikiRoot, ".claims") }

// RunPath is the resumable run checkpoint.
func (l Layout) RunPath() string { return filepath.Join(l.WikiRoot, ".run.json") }

// ManifestPath is the page manifest.
func (l Layout) ManifestPath() string { return filepath.Join(l.WikiRoot, ".page-manifest.json") }

// LastUpdatePath is the last-run metadata file.
func (l Layout) LastUpdatePath() string { return filepath.Join(l.WikiRoot, ".last-update.json") }

// InstructionsPath is the user-owned authoring guidance file.
func (l Layout) InstructionsPath() string { return filepath.Join(l.WikiRoot, "INSTRUCTIONS.md") }

// PageID converts a wiki-relative page path ("concepts/x.md") to the
// canonical identifier "/openwiki/concepts/x.md".
func PageID(rel string) string {
	return "/" + WikiDirName + "/" + strings.TrimLeft(path.Clean("/"+filepath.ToSlash(rel)), "/")
}

// PageRel converts a canonical page identifier back to a wiki-relative path.
// It reports false for identifiers outside the wiki.
func PageRel(id string) (string, bool) {
	rel, ok := strings.CutPrefix(id, "/"+WikiDirName+"/")
	if !ok || rel == "" || path.Clean("/"+rel) != "/"+rel {
		return "", false
	}
	return rel, true
}

// PagePath returns the absolute file path of a canonical page identifier.
func (l Layout) PagePath(id string) (string, error) {
	rel, ok := PageRel(id)
	if !ok {
		return "", fmt.Errorf("invalid page %q", id)
	}
	return filepath.Join(l.WikiRoot, filepath.FromSlash(rel)), nil
}

// RepoRoot returns the absolute, symlink-free Git top level containing dir.
func RepoRoot(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%s is not inside a Git repository: %s", dir, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("%s is not inside a Git repository: %w", dir, err)
	}
	root, err := filepath.Abs(strings.TrimSpace(string(out)))
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(root)
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// ExternalID is the stable directory name of an external wiki: a readable
// slug of the repository name plus a short hash of its root path.
func ExternalID(repoRoot string) string {
	sum := sha256.Sum256([]byte(repoRoot))
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(filepath.Base(repoRoot)), "-"), "-")
	if slug == "" {
		slug = "repo"
	}
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	return slug + "-" + hex.EncodeToString(sum[:])[:12]
}
