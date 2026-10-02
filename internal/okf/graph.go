package okf

import (
	"path"
	"strings"
)

// PageLinks returns the concept pages that page links to, as page ids, in
// order of first appearance. Images, external links, links outside the wiki,
// links to directories or missing pages, and links to the page itself are
// left out; known holds the concept page ids that count as targets.
func PageLinks(page, content string, known map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(stripStamps(content), "\n") {
		for _, m := range markdownLink.FindAllStringSubmatchIndex(line, -1) {
			if m[0] > 0 && line[m[0]-1] == '!' {
				continue // image
			}
			href := strings.TrimSpace(line[m[4]:m[5]])
			if href == "" || externalHref.MatchString(href) {
				continue
			}
			dest := strings.TrimSpace(linkTitle.ReplaceAllString(href, ""))
			target, _, _ := strings.Cut(dest, "#")
			if target == "" || strings.HasPrefix(target, "/") || strings.HasSuffix(target, "/") {
				continue
			}
			resolved := path.Clean(path.Join(path.Dir(page), decode(target)))
			if resolved == page || !known[resolved] || seen[resolved] {
				continue
			}
			seen[resolved] = true
			out = append(out, resolved)
		}
	}
	return out
}
