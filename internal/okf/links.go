package okf

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"owcli/internal/store"
)

var (
	markdownLink    = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
	headingLine     = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	brokenLinkStamp = regexp.MustCompile(`^\s*<!--\s*openwiki:\s*broken internal link\b.*?-->\s*$`)
	externalHref    = regexp.MustCompile(`(?i)^(?:[a-z][a-z\d+.-]*:|//)`)
	linkTitle       = regexp.MustCompile(`\s+(?:"[^"]*"|'[^']*')\s*$`)
)

// LinkIssue is one broken internal link.
type LinkIssue struct {
	Page    string
	Line    int // 1-based
	Href    string
	Message string
}

// LinkReport summarizes a link validation pass.
type LinkReport struct {
	FilesScanned, LinksChecked int
	Issues                     []LinkIssue
	StampedFiles               []string
}

// ValidateLinks checks relative links and heading anchors in every concept
// page. Broken links are not fatal: each gets an HTML comment stamped above
// its line, and stale stamps from earlier passes are removed first, so a
// fixed link leaves nothing behind.
//
// Link targets are resolved in the repository's coordinate space with the
// wiki at /openwiki, so a page may link to source files ("../src/x.go") in
// either storage layout.
func (w Wiki) ValidateLinks() (LinkReport, error) {
	var r LinkReport
	pages, err := w.ConceptPages()
	if err != nil {
		return r, err
	}
	for _, p := range pages {
		original, err := w.Read(p)
		if err != nil {
			return r, err
		}
		r.FilesScanned++
		cleaned := stripStamps(original)
		own := anchors(cleaned)
		var issues []LinkIssue
		for i, line := range strings.Split(cleaned, "\n") {
			for _, m := range markdownLink.FindAllStringSubmatchIndex(line, -1) {
				if m[0] > 0 && line[m[0]-1] == '!' {
					continue // image
				}
				r.LinksChecked++
				href := strings.TrimSpace(line[m[4]:m[5]])
				if msg := w.checkLink(p, href, own); msg != "" {
					issues = append(issues, LinkIssue{Page: p, Line: i + 1, Href: href, Message: msg})
				}
			}
		}
		r.Issues = append(r.Issues, issues...)
		stamped := stamp(cleaned, issues)
		if stamped == original {
			continue
		}
		if err := w.Write(p, stamped); err != nil {
			return r, err
		}
		r.StampedFiles = append(r.StampedFiles, p)
	}
	return r, nil
}

// checkLink returns a problem description, or "" for a good link.
func (w Wiki) checkLink(page, href string, own map[string]bool) string {
	if href == "" || externalHref.MatchString(href) {
		return ""
	}
	dest := strings.TrimSpace(linkTitle.ReplaceAllString(href, ""))
	target, anchor, hasAnchor := strings.Cut(dest, "#")
	if target == "" {
		if hasAnchor && anchor != "" && !own[decode(anchor)] {
			return fmt.Sprintf("heading anchor %q does not exist in %s", anchor, page)
		}
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return fmt.Sprintf("link %q is root-absolute, which no real consumer resolves against the repository root (not a coding agent reading the page, not GitHub's Markdown renderer, not a local viewer); use a path relative to this file instead", target)
	}
	isDir := strings.HasSuffix(target, "/")
	resolved := path.Clean(path.Join(path.Dir(page), decode(target)))
	abs := w.virtualToAbs(resolved)
	fi, err := os.Stat(abs)
	if err != nil || (isDir && !fi.IsDir()) {
		if isDir {
			return fmt.Sprintf("directory %q does not exist", target)
		}
		return fmt.Sprintf("file %q does not exist", target)
	}
	if !hasAnchor || anchor == "" || isDir || fi.IsDir() || !strings.EqualFold(path.Ext(resolved), ".md") {
		return ""
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf("file %q cannot be read", target)
	}
	if !anchors(string(data))[decode(anchor)] {
		return fmt.Sprintf("heading anchor %q does not exist in %q", anchor, target)
	}
	return ""
}

// virtualToAbs maps a repository-space path ("/openwiki/x.md", "/src/a.go")
// to a filesystem path: wiki paths go to the wiki root, others to the repo.
func (w Wiki) virtualToAbs(v string) string {
	if rel, ok := store.PageRel(v); ok {
		return filepath.Join(w.Root, filepath.FromSlash(rel))
	}
	if v == "/"+store.WikiDirName {
		return w.Root
	}
	repo := w.Repo
	if repo == "" {
		repo = filepath.Dir(w.Root)
	}
	return filepath.Join(repo, filepath.FromSlash(strings.TrimPrefix(v, "/")))
}

func decode(s string) string {
	if d, err := url.PathUnescape(s); err == nil {
		return d
	}
	return s
}

func stripStamps(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	kept := lines[:0]
	for _, l := range lines {
		if !brokenLinkStamp.MatchString(l) {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

func stamp(content string, issues []LinkIssue) string {
	if len(issues) == 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	byLine := map[int][]string{}
	var order []int
	for _, is := range issues {
		if _, ok := byLine[is.Line]; !ok {
			order = append(order, is.Line)
		}
		byLine[is.Line] = append(byLine[is.Line], fmt.Sprintf("<!-- openwiki: broken internal link [%s] %s. Fix the href or restore the target, then delete this comment. -->", is.Href, is.Message))
	}
	sort.Sort(sort.Reverse(sort.IntSlice(order)))
	for _, n := range order {
		tail := append(append([]string(nil), byLine[n]...), lines[n-1:]...)
		lines = append(lines[:n-1], tail...)
	}
	return strings.Join(lines, "\n")
}

// anchors returns the GitHub-style heading anchors of a document.
func anchors(content string) map[string]bool {
	out := map[string]bool{}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		m := headingLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		base := Slug(m[2])
		if base == "" {
			continue
		}
		n := counts[base]
		counts[base] = n + 1
		if n == 0 {
			out[base] = true
		} else {
			out[fmt.Sprintf("%s-%d", base, n)] = true
		}
	}
	return out
}

// Slug converts heading text to a GitHub-style anchor: lowercase, keep
// letters, marks, numbers, whitespace, "_" and "-", then each whitespace
// character becomes "-" (runs are not collapsed).
func Slug(text string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case unicode.IsSpace(c):
			b.WriteByte('-')
		case unicode.IsLetter(c) || unicode.IsMark(c) || unicode.IsNumber(c) || c == '_' || c == '-':
			b.WriteRune(c)
		}
	}
	return b.String()
}
