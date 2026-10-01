// Package ignore implements .openwikiignore, the gitignore-like rule file
// that keeps repository paths out of agent reads, claim evidence, and source
// fingerprints.
//
// Rules are evaluated in order and the last matching rule wins, so a later
// "!pattern" can re-include a path an earlier rule ignored, including a path
// below an ignored directory. Matching is case-insensitive.
package ignore

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// FileName is the rule file read from the repository root.
const FileName = ".openwikiignore"

// Rule is one compiled pattern.
type Rule struct {
	pattern    string
	negated    bool
	dirOnly    bool
	self       *regexp.Regexp // matches a path the pattern names directly
	descendant *regexp.Regexp // matches paths below such a path
}

// Compile turns one pattern into a rule. It reports false for patterns that
// match nothing, such as "/" or "!".
func Compile(pattern string) (Rule, bool) {
	p := strings.ReplaceAll(pattern, `\`, "/")
	r := Rule{pattern: pattern}
	if strings.HasPrefix(p, "!") {
		r.negated = true
		p = p[1:]
	}
	for strings.HasPrefix(p, "./") {
		p = strings.TrimLeft(p[2:], "/")
	}
	p = collapseSlashes(p)
	anchored := strings.HasPrefix(p, "/")
	r.dirOnly = strings.HasSuffix(p, "/")
	p = strings.Trim(p, "/")
	if p == "" {
		return Rule{}, false
	}

	prefix := "(?:^|/)"
	if anchored || strings.Contains(p, "/") {
		prefix = "^"
	}
	src := globToRegexp(p)
	r.self = regexp.MustCompile("(?i)" + prefix + src + "$")
	r.descendant = regexp.MustCompile("(?i)" + prefix + src + "/")
	return r, true
}

// Matches reports whether the rule applies to a normalized repository-relative
// path. A directory-only rule applies to directories it names and to anything
// below them, but not to a file with the same name.
func (r Rule) Matches(rel string, isDir bool) bool {
	if r.descendant.MatchString(rel) {
		return true
	}
	return r.self.MatchString(rel) && (isDir || !r.dirOnly)
}

// Negated reports whether the rule re-includes paths.
func (r Rule) Negated() bool { return r.negated }

// String returns the pattern as written.
func (r Rule) String() string { return r.pattern }

// Matcher evaluates a rule list plus fixed exclusions that rules cannot undo.
type Matcher struct {
	rules     []Rule
	negations bool
	fixed     []string
}

// Parse builds a matcher from rule-file contents. Blank lines and lines
// starting with "#" are skipped; surrounding whitespace is trimmed.
func Parse(contents string) *Matcher {
	m := &Matcher{fixed: []string{".git"}}
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if r, ok := Compile(line); ok {
			m.rules = append(m.rules, r)
			m.negations = m.negations || r.negated
		}
	}
	return m
}

// Load reads FileName from repoRoot. A missing file yields a matcher with no
// rules (only the fixed .git exclusion).
func Load(repoRoot string) (*Matcher, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, FileName))
	if errors.Is(err, fs.ErrNotExist) {
		return Parse(""), nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(string(data)), nil
}

// Exclude adds repository-relative directories that are always ignored,
// whatever the rules say. The returned matcher shares no state with m.
// Callers use it for the in-repo wiki root.
func (m *Matcher) Exclude(dirs ...string) *Matcher {
	out := *m
	out.fixed = append([]string(nil), m.fixed...)
	for _, d := range dirs {
		if d = Normalize(d); d != "" {
			out.fixed = append(out.fixed, d)
		}
	}
	return &out
}

// Active reports whether the rule file contributed any rules.
func (m *Matcher) Active() bool { return len(m.rules) > 0 }

// Rules returns the compiled rules in evaluation order.
func (m *Matcher) Rules() []Rule { return append([]Rule(nil), m.rules...) }

// Ignores reports whether a repository-relative path is excluded. The empty
// path (the repository root) is never ignored.
func (m *Matcher) Ignores(rel string, isDir bool) bool {
	rel = Normalize(rel)
	if rel == "" {
		return false
	}
	for _, d := range m.fixed {
		if strings.EqualFold(rel, d) || hasPrefixFold(rel, d+"/") {
			return true
		}
	}
	ignored := false
	for _, r := range m.rules {
		if r.Matches(rel, isDir) {
			ignored = !r.negated
		}
	}
	return ignored
}

// SkipDir reports whether a directory walk may prune dir: it is ignored and
// nothing below it can be re-included by a negated rule.
func (m *Matcher) SkipDir(rel string) bool {
	if !m.Ignores(rel, true) {
		return false
	}
	if !m.negations {
		return true
	}
	rel = Normalize(rel)
	for _, d := range m.fixed {
		if strings.EqualFold(rel, d) || hasPrefixFold(rel, d+"/") {
			return true
		}
	}
	return false
}

// Normalize converts a path to the slash-separated, root-relative form rules
// are matched against: no leading or trailing slash, no "." or ".." segments.
func Normalize(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	p = path.Clean("/" + strings.TrimLeft(p, "/"))
	return strings.Trim(p, "/")
}

func globToRegexp(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '*' && i+1 < len(p) && p[i+1] == '*':
			if i+2 < len(p) && p[i+2] == '/' {
				b.WriteString("(?:.*/)?")
				i += 2
			} else {
				b.WriteString(".*")
				i++
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

func collapseSlashes(p string) string {
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return p
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
