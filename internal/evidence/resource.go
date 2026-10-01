// Package evidence addresses repository source with repo:// resources and
// resolves them to content-derived version tokens.
//
// A version mismatch means the cited content changed; a nil resolution means
// the file or range no longer exists. Line-range versions carry relocation
// anchors so moved-but-unchanged text keeps its version.
package evidence

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Prefix starts every repository evidence resource.
const Prefix = "repo://"

// Errors are wrapped so callers can classify failures with errors.Is.
var (
	// ErrInvalidResource is malformed or disallowed input; the caller can fix it.
	ErrInvalidResource = errors.New("invalid evidence resource")
	// ErrSecurity is a containment refusal (symlink or filesystem alias).
	// Preflight treats it as unresolved evidence rather than aborting.
	ErrSecurity = errors.New("evidence containment violation")
)

// excludedRoots are never evidence: Git metadata and generated wiki output.
var excludedRoots = []string{".git", "openwiki"}

// Range is an inclusive, 1-based line range.
type Range struct {
	Start, End int
}

// Resource is a parsed repo:// URI.
type Resource struct {
	Path  string // normalized repository-relative path, slash-separated
	Range *Range // nil for whole-file evidence
}

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidResource, fmt.Sprintf(format, args...))
}

var lineFragment = regexp.MustCompile(`^L([1-9]\d*)(?:-L([1-9]\d*))?$`)

// Parse validates and normalizes a resource. "#L8" becomes the range 8-8.
func Parse(s string) (Resource, error) {
	body, ok := strings.CutPrefix(s, Prefix)
	if !ok {
		return Resource{}, invalidf("unsupported evidence resource %q (use repo://<path>[#Lx-Ly])", s)
	}
	encPath, encFrag, hasFrag := strings.Cut(body, "#")
	if strings.Contains(encFrag, "#") {
		return Resource{}, invalidf("unescaped fragment delimiter in %q", s)
	}
	p, err := decodeComponent(encPath)
	if err != nil {
		return Resource{}, invalidf("invalid percent encoding in %q", s)
	}
	if hasControl(p) {
		return Resource{}, invalidf("control character in path of %q", s)
	}
	norm := strings.TrimPrefix(path.Clean(strings.ReplaceAll(p, `\`, "/")), "./")
	if norm == "" || norm == "." || norm == ".." || strings.HasPrefix(norm, "../") ||
		strings.HasPrefix(norm, "/") || isDrivePath(norm) {
		return Resource{}, invalidf("evidence path must stay inside the repository: %q", s)
	}
	lower := strings.ToLower(norm)
	for _, root := range excludedRoots {
		if lower == root || strings.HasPrefix(lower, root+"/") {
			return Resource{}, invalidf("evidence cannot reference Git metadata or generated wiki output: %q", s)
		}
	}
	r := Resource{Path: norm}
	if hasFrag {
		frag, err := decodeComponent(encFrag)
		if err != nil {
			return Resource{}, invalidf("invalid percent encoding in %q", s)
		}
		m := lineFragment.FindStringSubmatch(frag)
		if m == nil {
			return Resource{}, invalidf("fragment must be a line range such as #L10-L24: %q", s)
		}
		start, err1 := strconv.Atoi(m[1])
		end, err2 := start, error(nil)
		if m[2] != "" {
			end, err2 = strconv.Atoi(m[2])
		}
		if err1 != nil || err2 != nil || start > maxLine || end > maxLine {
			return Resource{}, invalidf("line range too large in %q", s)
		}
		if end < start {
			return Resource{}, invalidf("line range must end at or after its start: %q", s)
		}
		r.Range = &Range{Start: start, End: end}
	}
	return r, nil
}

// maxLine matches the largest integer upstream accepts exactly (2^53-1).
const maxLine = 1<<53 - 1

// String renders the canonical form: each path segment percent-encoded like
// JavaScript's encodeURIComponent, and ranges as #Lstart-Lend.
func (r Resource) String() string {
	segs := strings.Split(r.Path, "/")
	for i, s := range segs {
		segs[i] = encodeComponent(s)
	}
	out := Prefix + strings.Join(segs, "/")
	if r.Range != nil {
		out += fmt.Sprintf("#L%d-L%d", r.Range.Start, r.Range.End)
	}
	return out
}

// WholeFile returns the resource without its line range.
func (r Resource) WholeFile() Resource { return Resource{Path: r.Path} }

// Canonical parses and re-renders s.
func Canonical(s string) (string, error) {
	r, err := Parse(s)
	if err != nil {
		return "", err
	}
	return r.String(), nil
}

func isDrivePath(p string) bool {
	return len(p) >= 3 && p[1] == ':' && p[2] == '/' &&
		(p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z')
}

func hasControl(s string) bool {
	for _, c := range s {
		if c <= 0x1f || c == 0x7f {
			return true
		}
	}
	return false
}

// encodeComponent percent-encodes everything except A-Z a-z 0-9 - _ . ! ~ * ' ( ).
func encodeComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// decodeComponent reverses percent-encoding and requires valid UTF-8.
func decodeComponent(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", errors.New("truncated escape")
		}
		v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
		if err != nil {
			return "", err
		}
		b.WriteByte(byte(v))
		i += 2
	}
	out := b.String()
	if !utf8.ValidString(out) {
		return "", errors.New("invalid UTF-8")
	}
	return out, nil
}
