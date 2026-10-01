package claims

import (
	"path"
	"strings"
)

// ClaimsDirName is the sidecar directory below the wiki root.
const ClaimsDirName = ".claims"

const wikiPrefix = "/openwiki/"

// reservedFiles are structural pages that never own Claims (compared
// case-insensitively).
var reservedFiles = map[string]bool{"index.md": true, "log.md": true, "instructions.md": true}

func hasTraversal(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// IsGroundedPage reports whether a canonical or loosely written page path is
// a concept page that can own Claims: a .md file below /openwiki/, outside
// .claims, and not index.md, log.md, or INSTRUCTIONS.md.
func IsGroundedPage(p string) bool {
	p = strings.ReplaceAll(p, `\`, "/")
	if hasTraversal(p) {
		return false
	}
	norm := path.Clean("/" + strings.TrimLeft(p, "/"))
	lower := strings.ToLower(norm)
	if !strings.HasPrefix(norm, wikiPrefix) || !strings.HasSuffix(norm, ".md") {
		return false
	}
	for _, seg := range strings.Split(lower, "/") {
		if seg == ClaimsDirName {
			return false
		}
	}
	return !reservedFiles[path.Base(lower)]
}

// NormalizePage returns the canonical "/openwiki/<path>.md" form of a grounded
// page, or ErrInvalid.
func NormalizePage(p string) (string, error) {
	slashed := strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if hasTraversal(slashed) {
		return "", invalidf("page cannot contain traversal segments: %s", p)
	}
	norm := path.Clean("/" + strings.TrimLeft(slashed, "/"))
	if !strings.HasPrefix(norm, wikiPrefix) || !strings.HasSuffix(norm, ".md") {
		return "", invalidf("page must be a Markdown file below /openwiki: %s", p)
	}
	if !IsGroundedPage(norm) {
		return "", invalidf("page is reserved or structural: %s", p)
	}
	return norm, nil
}

// NormalizeToolPage accepts the looser forms a model may write
// ("concepts/x.md", "openwiki/concepts/x.md", "/openwiki/concepts/x.md") and
// returns the canonical page.
func NormalizeToolPage(p string) (string, error) {
	slashed := strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if hasTraversal(slashed) {
		return "", invalidf("page cannot contain traversal segments: %s", p)
	}
	unrooted := strings.TrimLeft(slashed, "/")
	if unrooted != "openwiki" && !strings.HasPrefix(unrooted, "openwiki/") {
		unrooted = "openwiki/" + unrooted
	}
	return NormalizePage(unrooted)
}

// sidecarRel maps "/openwiki/a/b.md" to "a/b.json".
func sidecarRel(page string) string {
	return strings.TrimSuffix(strings.TrimPrefix(page, wikiPrefix), ".md") + ".json"
}

// pageRel maps "/openwiki/a/b.md" to "a/b.md".
func pageRel(page string) string { return strings.TrimPrefix(page, wikiPrefix) }
