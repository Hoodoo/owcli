package evidence

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"owcli/internal/ignore"
)

// Version token prefixes.
const (
	FileVersionPrefix  = "repo-file-v1:sha256:"
	LinesVersionPrefix = "repo-lines-v1:sha256:"
)

// contextLines is how many lines around a range the anchors record.
const contextLines = 3

// Evidence is one versioned citation.
type Evidence struct {
	Resource string `json:"resource"`
	Version  string `json:"version"`
}

// Resolved is the current state of a resource. Its Resource may differ from
// the requested one when a line range was relocated.
type Resolved struct {
	Evidence Evidence
	Content  string
}

// Resolver turns a resource into its current version. A nil result with a nil
// error means the file or range no longer exists. previousVersion, when
// given, lets line ranges be relocated.
type Resolver interface {
	Resolve(resource, previousVersion string) (*Resolved, error)
}

// RepoResolver resolves repo:// resources against one repository checkout.
type RepoResolver struct {
	root     string
	realRoot string
	ignore   *ignore.Matcher
}

// NewRepoResolver builds a resolver rooted at an absolute repository path.
// Paths ignored by m are refused as invalid evidence.
func NewRepoResolver(root string, m *ignore.Matcher) (*RepoResolver, error) {
	if !filepath.IsAbs(root) {
		return nil, invalidf("repository root %q must be absolute", root)
	}
	root = filepath.Clean(root)
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve repository root %s: %v", ErrSecurity, root, err)
	}
	if m == nil {
		m = ignore.Parse("")
	}
	return &RepoResolver{root: root, realRoot: real, ignore: m}, nil
}

// Resolve implements Resolver.
func (r *RepoResolver) Resolve(resource, previousVersion string) (*Resolved, error) {
	res, err := Parse(resource)
	if err != nil {
		return nil, err
	}
	if r.ignore.Ignores(res.Path, false) {
		return nil, invalidf("evidence path is excluded by %s: %s", ignore.FileName, res.Path)
	}
	source, found, err := r.read(res.Path)
	if err != nil || !found {
		return nil, err
	}
	if res.Range == nil {
		return &Resolved{
			Evidence: Evidence{Resource: res.String(), Version: FileVersionPrefix + hashText(source)},
			Content:  source,
		}, nil
	}
	return resolveRange(res, source, previousVersion), nil
}

// read loads a regular file through the containment gate.
func (r *RepoResolver) read(rel string) (string, bool, error) {
	abs := filepath.Join(r.root, filepath.FromSlash(rel))
	fi, err := os.Lstat(abs)
	if isMissing(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read evidence %s: %w", rel, err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return "", false, fmt.Errorf("%w: evidence cannot reference a symbolic link: %s", ErrSecurity, rel)
	}
	if !fi.Mode().IsRegular() {
		return "", false, nil
	}
	physical, err := filepath.EvalSymlinks(abs)
	if isMissing(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read evidence %s: %w", rel, err)
	}
	if physical != filepath.Join(r.realRoot, filepath.FromSlash(rel)) {
		return "", false, fmt.Errorf("%w: evidence path traverses a symbolic link or filesystem alias: %s", ErrSecurity, rel)
	}
	data, err := os.ReadFile(physical)
	if isMissing(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read evidence %s: %w", rel, err)
	}
	return string(data), true, nil
}

func isMissing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// --- line ranges ---

// anchors are the relocation metadata embedded in a line-range version.
// Field order is part of the token format.
type anchors struct {
	SelectedLineCount         int    `json:"selectedLineCount"`
	FirstSelectedLineHash     string `json:"firstSelectedLineHash"`
	LastSelectedLineHash      string `json:"lastSelectedLineHash"`
	PrecedingContextLineCount int    `json:"precedingContextLineCount"`
	PrecedingContextHash      string `json:"precedingContextHash"`
	FollowingContextLineCount int    `json:"followingContextLineCount"`
	FollowingContextHash      string `json:"followingContextHash"`
}

// span is a half-open range of line indexes.
type span struct{ start, end int }

func resolveRange(res Resource, source, previousVersion string) *Resolved {
	lines := splitLines(source)
	var hinted *span
	if res.Range.End <= len(lines) {
		hinted = &span{res.Range.Start - 1, res.Range.End}
	}
	prevHash, prev, ok := parseLinesVersion(previousVersion)
	if !ok {
		if hinted == nil {
			return nil
		}
		return newRangeEvidence(res.Path, lines, *hinted)
	}
	if s := locateUnchanged(lines, hinted, prevHash, prev); s != nil {
		return &Resolved{
			Evidence: Evidence{Resource: rangeResource(res.Path, *s), Version: previousVersion},
			Content:  join(lines, *s),
		}
	}
	if s := locateChanged(lines, prev); s != nil {
		return newRangeEvidence(res.Path, lines, *s)
	}
	return nil
}

// splitLines splits after each "\n", keeping terminators; a trailing
// newline does not start an extra line.
func splitLines(s string) []string {
	var lines []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			lines = append(lines, s)
			break
		}
		lines = append(lines, s[:i+1])
		s = s[i+1:]
	}
	return lines
}

func join(lines []string, s span) string { return strings.Join(lines[s.start:s.end], "") }

func rangeResource(p string, s span) string {
	return Resource{Path: p, Range: &Range{Start: s.start + 1, End: s.end}}.String()
}

func newRangeEvidence(p string, lines []string, s span) *Resolved {
	content := join(lines, s)
	before := max(0, s.start-contextLines)
	after := min(len(lines), s.end+contextLines)
	a := anchors{
		SelectedLineCount:         s.end - s.start,
		FirstSelectedLineHash:     hashText(lines[s.start]),
		LastSelectedLineHash:      hashText(lines[s.end-1]),
		PrecedingContextLineCount: s.start - before,
		PrecedingContextHash:      hashText(join(lines, span{before, s.start})),
		FollowingContextLineCount: after - s.end,
		FollowingContextHash:      hashText(join(lines, span{s.end, after})),
	}
	meta, _ := json.Marshal(a)
	return &Resolved{
		Evidence: Evidence{
			Resource: rangeResource(p, s),
			Version:  LinesVersionPrefix + hashText(content) + ":" + base64.RawURLEncoding.EncodeToString(meta),
		},
		Content: content,
	}
}

var sha256Hex = regexp.MustCompile(`^[a-f0-9]{64}$`)

func parseLinesVersion(v string) (string, anchors, bool) {
	body, ok := strings.CutPrefix(v, LinesVersionPrefix)
	if !ok {
		return "", anchors{}, false
	}
	hash, encoded, ok := strings.Cut(body, ":")
	if !ok || !sha256Hex.MatchString(hash) {
		return "", anchors{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encoded, "="))
	if err != nil {
		return "", anchors{}, false
	}
	var keys map[string]json.RawMessage
	var a anchors
	if json.Unmarshal(raw, &keys) != nil || len(keys) != 7 || json.Unmarshal(raw, &a) != nil {
		return "", anchors{}, false
	}
	valid := a.SelectedLineCount >= 1 &&
		a.PrecedingContextLineCount >= 0 && a.PrecedingContextLineCount <= contextLines &&
		a.FollowingContextLineCount >= 0 && a.FollowingContextLineCount <= contextLines
	for _, h := range []string{a.FirstSelectedLineHash, a.LastSelectedLineHash, a.PrecedingContextHash, a.FollowingContextHash} {
		valid = valid && sha256Hex.MatchString(h)
	}
	return hash, a, valid
}

// locateUnchanged finds the previously cited text, unchanged: at the hinted
// span, else at the unique span whose first/last lines and content match,
// else at the unique such span whose context also matches.
func locateUnchanged(lines []string, hinted *span, contentHash string, a anchors) *span {
	if hinted != nil && hinted.end-hinted.start == a.SelectedLineCount && hashText(join(lines, *hinted)) == contentHash {
		return hinted
	}
	hashes := make([]string, len(lines))
	for i, l := range lines {
		hashes[i] = hashText(l)
	}
	var matches []span
	for start := 0; start+a.SelectedLineCount <= len(lines); start++ {
		s := span{start, start + a.SelectedLineCount}
		if hashes[s.start] == a.FirstSelectedLineHash && hashes[s.end-1] == a.LastSelectedLineHash &&
			hashText(join(lines, s)) == contentHash {
			matches = append(matches, s)
		}
	}
	if len(matches) == 1 {
		return &matches[0]
	}
	var withContext []span
	for _, s := range matches {
		if contextMatches(lines, s, a) {
			withContext = append(withContext, s)
		}
	}
	if len(withContext) == 1 {
		return &withContext[0]
	}
	return nil
}

func contextMatches(lines []string, s span, a anchors) bool {
	var before, after bool
	if a.PrecedingContextLineCount == 0 {
		before = s.start == 0
	} else {
		before = s.start >= a.PrecedingContextLineCount &&
			hashText(join(lines, span{s.start - a.PrecedingContextLineCount, s.start})) == a.PrecedingContextHash
	}
	if a.FollowingContextLineCount == 0 {
		after = s.end == len(lines)
	} else {
		after = s.end+a.FollowingContextLineCount <= len(lines) &&
			hashText(join(lines, span{s.end, s.end + a.FollowingContextLineCount})) == a.FollowingContextHash
	}
	return before && after
}

// locateChanged finds the region between the unique surviving preceding and
// following context, when the cited text itself changed.
func locateChanged(lines []string, a anchors) *span {
	starts := contextBoundaries(lines, a.PrecedingContextLineCount, a.PrecedingContextHash, true)
	ends := contextBoundaries(lines, a.FollowingContextLineCount, a.FollowingContextHash, false)
	var found *span
	for _, s := range starts {
		for _, e := range ends {
			if e > s {
				if found != nil {
					return nil
				}
				found = &span{s, e}
			}
		}
	}
	return found
}

func contextBoundaries(lines []string, count int, hash string, before bool) []int {
	if count == 0 {
		if before {
			return []int{0}
		}
		return []int{len(lines)}
	}
	var out []int
	for start := 0; start+count <= len(lines); start++ {
		if hashText(join(lines, span{start, start + count})) == hash {
			if before {
				out = append(out, start+count)
			} else {
				out = append(out, start)
			}
		}
	}
	return out
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// --- caching ---

// cache memoizes resolutions for one processing phase.
type cache struct {
	inner Resolver
	seen  map[[2]string]cached
}

type cached struct {
	res *Resolved
	err error
}

// Cached wraps r so each (resource, previousVersion) pair resolves at most
// once. Create a fresh wrapper per phase (preflight, one mutation batch, one
// finalization) so results never cross a freshness boundary.
func Cached(r Resolver) Resolver {
	return &cache{inner: r, seen: map[[2]string]cached{}}
}

func (c *cache) Resolve(resource, previousVersion string) (*Resolved, error) {
	key := [2]string{resource, previousVersion}
	if v, ok := c.seen[key]; ok {
		return v.res, v.err
	}
	res, err := c.inner.Resolve(resource, previousVersion)
	c.seen[key] = cached{res, err}
	return res, err
}
