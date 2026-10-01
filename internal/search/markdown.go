package search

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// heading is one top-level Markdown heading with its source span.
type heading struct {
	depth  int
	text   string // heading source text ("`code` title")
	anchor string // GitHub-style slug, deduplicated with -N suffixes
	start  int    // offset of the heading's first line
	end    int    // offset just past the heading (including a setext underline)
}

// section is a heading plus everything up to the next heading of the same or
// higher level.
type section struct {
	heading
	raw string // trimmed source of the whole section, heading included
}

var setextUnderline = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)

// headings lists the document's top-level headings (not those inside block
// quotes, lists, or code) in order.
func headings(src []byte) []heading {
	doc := goldmark.DefaultParser().Parse(text.NewReader(src))
	var out []heading
	counts := map[string]int{}
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		h, ok := n.(*ast.Heading)
		if !ok || h.Lines().Len() == 0 {
			continue
		}
		first, last := h.Lines().At(0), h.Lines().At(h.Lines().Len()-1)
		start := lineStart(src, first.Start)
		end := lineEnd(src, last.Stop)
		// A setext heading's underline follows its text lines.
		if next := lineEnd(src, end); end < len(src) && setextUnderline.Match(trimEOL(src[end:next])) {
			end = next
		}
		var raw []string
		for i := 0; i < h.Lines().Len(); i++ {
			seg := h.Lines().At(i)
			raw = append(raw, string(seg.Value(src)))
		}
		base := slug(inlineText(h, src))
		anchor := base
		if c := counts[base]; c > 0 {
			anchor = fmt.Sprintf("%s-%d", base, c)
		}
		counts[base]++
		out = append(out, heading{
			depth:  h.Level,
			text:   strings.TrimSpace(strings.Join(raw, "\n")),
			anchor: anchor,
			start:  start,
			end:    end,
		})
	}
	return out
}

// sections pairs each heading with its full section text.
func sections(src []byte, hs []heading) []section {
	out := make([]section, len(hs))
	for i, h := range hs {
		stop := len(src)
		for _, next := range hs[i+1:] {
			if next.depth <= h.depth {
				stop = next.start
				break
			}
		}
		out[i] = section{heading: h, raw: strings.TrimSpace(string(src[h.start:stop]))}
	}
	return out
}

func lineStart(src []byte, i int) int {
	for i > 0 && src[i-1] != '\n' {
		i--
	}
	return i
}

func lineEnd(src []byte, i int) int {
	for i < len(src) && src[i] != '\n' {
		i++
	}
	if i < len(src) {
		i++
	}
	return i
}

func trimEOL(b []byte) []byte {
	return []byte(strings.TrimRight(string(b), "\r\n"))
}

// inlineText renders a heading's inline content as plain text: code spans
// keep their content, links keep their text, emphasis markers disappear.
func inlineText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(src))
			if t.SoftLineBreak() || t.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(t.Value)
		case *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

var slugDrop = regexp.MustCompile(`[^\p{L}\p{N}_\s-]`)

// slug lowercases, drops everything but letters, numbers, "_", "-" and
// whitespace, and turns each space into "-".
func slug(s string) string {
	return strings.ReplaceAll(slugDrop.ReplaceAllString(strings.ToLower(s), ""), " ", "-")
}

var fence = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")

// blocks splits Markdown into blank-line-separated blocks, keeping fenced
// code intact and dropping single-line ATX headings.
func blocks(md string) []string {
	var out, cur []string
	inFence := ""
	flush := func() {
		if len(cur) > 0 {
			b := strings.Join(cur, "\n")
			if !(len(cur) == 1 && strings.HasPrefix(strings.TrimLeft(cur[0], " "), "#")) && strings.TrimSpace(b) != "" {
				out = append(out, b)
			}
			cur = nil
		}
	}
	for _, line := range strings.Split(md, "\n") {
		if inFence != "" {
			cur = append(cur, line)
			if m := fence.FindStringSubmatch(line); m != nil && strings.HasPrefix(m[1], inFence[:1]) && len(m[1]) >= len(inFence) {
				inFence = ""
				flush()
			}
			continue
		}
		if m := fence.FindStringSubmatch(line); m != nil {
			flush()
			inFence = m[1]
			cur = append(cur, line)
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(line, " "), "#") {
			flush()
			cur = append(cur, line)
			flush()
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return out
}
