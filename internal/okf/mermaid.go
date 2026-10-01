package okf

import (
	"regexp"
	"strings"
)

// MermaidFence is one ```mermaid block.
type MermaidFence struct {
	OpenLine, CloseLine int // 0-based line indexes of the fence markers
	Indent, Marker      string
	Body                string
}

var fenceLine = regexp.MustCompile("^(\\s*)(`{3,})\\s*(\\S*)\\s*$")

// ExtractMermaid finds mermaid fences, ignoring fences nested inside other
// (longer or info-tagged) code fences.
func ExtractMermaid(markdown string) []MermaidFence {
	lines := strings.Split(markdown, "\n")
	var out []MermaidFence
	var open *MermaidFence
	var body []string
	generic := ""
	for i, line := range lines {
		m := fenceLine.FindStringSubmatch(line)
		switch {
		case open != nil:
			if m != nil && len(m[2]) >= len(open.Marker) && m[3] == "" {
				open.CloseLine, open.Body = i, strings.Join(body, "\n")
				out = append(out, *open)
				open, body = nil, nil
			} else {
				body = append(body, line)
			}
		case generic != "":
			if m != nil && len(m[2]) >= len(generic) && m[3] == "" {
				generic = ""
			}
		case m != nil && strings.EqualFold(m[3], "mermaid"):
			open = &MermaidFence{OpenLine: i, Indent: m[1], Marker: m[2]}
		case m != nil && m[3] != "":
			generic = m[2]
		}
	}
	return out
}

var (
	flowchartEndNode  = regexp.MustCompile(`(?:^|\s)end\s*[\[({]`)
	flowchartEndArrow = regexp.MustCompile(`(?m)-->\s*end\s*(?:$|;)`)
	labelSemicolon    = regexp.MustCompile(`[\[({][^)\]}]*;[^)\]}]*[)\]}]`)
	labelAngle        = regexp.MustCompile(`[\[({][^)\]}]*[<>][^)\]}]*[)\]}]`)
	// Constructs that make ";" or "<" ">" legal inside a flowchart label.
	quotedText = regexp.MustCompile(`"[^"\n]*"`)
	htmlEntity = regexp.MustCompile(`&(?:#\d+|#x[0-9a-fA-F]+|[a-zA-Z]+);`)
	lineBreak  = regexp.MustCompile(`(?i)<br\s*/?>`)
)

// MermaidHeuristic flags only near-certain flowchart breakage, so a valid
// diagram is never degraded: a node named end, or a semicolon or angle bracket
// inside an unquoted label. Quoted labels, HTML entities, and <br> tags are
// legal and ignored; other diagram types are not checked. It returns "" for a
// diagram it accepts.
func MermaidHeuristic(body string) string {
	fields := strings.Fields(body)
	first := ""
	if len(fields) > 0 {
		first = strings.ToLower(fields[0])
	}
	if first != "flowchart" && first != "graph" {
		return ""
	}
	if flowchartEndNode.MatchString(body) || flowchartEndArrow.MatchString(body) {
		return "Heuristic: `end` is a reserved word and cannot be a flowchart node id; rename the node."
	}
	unquoted := lineBreak.ReplaceAllString(htmlEntity.ReplaceAllString(quotedText.ReplaceAllString(body, `""`), ""), " ")
	if labelSemicolon.MatchString(unquoted) {
		return "Heuristic: a semicolon inside a label breaks rendering; rephrase the label."
	}
	if labelAngle.MatchString(unquoted) {
		return "Heuristic: an unescaped angle bracket inside a label breaks rendering; rephrase the label."
	}
	return ""
}

// MermaidCommentPrefix starts the comment left above a degraded diagram. It
// matches upstream so either tool's update run can find and repair it.
const MermaidCommentPrefix = "<!-- openwiki: mermaid parse failed"

// DegradeMermaid converts invalid mermaid fences into text fences preceded
// by an explanatory comment. It returns the content and how many fences it
// degraded; valid documents are returned unchanged.
func DegradeMermaid(markdown string) (string, int) {
	fences := ExtractMermaid(markdown)
	type bad struct {
		f      MermaidFence
		reason string
	}
	var broken []bad
	for _, f := range fences {
		if r := MermaidHeuristic(f.Body); r != "" {
			broken = append(broken, bad{f, sanitizeDiagnostic(r)})
		}
	}
	if len(broken) == 0 {
		return markdown, 0
	}
	lines := strings.Split(markdown, "\n")
	for i := len(broken) - 1; i >= 0; i-- {
		f := broken[i].f
		replacement := []string{
			f.Indent + MermaidCommentPrefix + " and this diagram was converted to a text fence so it does not break rendering. Fix the diagram source and restore the mermaid fence. Parser error: " + broken[i].reason + " -->",
			f.Indent + f.Marker + "text",
		}
		replacement = append(replacement, strings.Split(f.Body, "\n")...)
		replacement = append(replacement, f.Indent+f.Marker)
		tail := append([]string(nil), lines[f.CloseLine+1:]...)
		lines = append(append(lines[:f.OpenLine], replacement...), tail...)
	}
	return strings.Join(lines, "\n"), len(broken)
}

const maxDiagnostic = 300

// sanitizeDiagnostic flattens a message to one line that cannot terminate an
// HTML comment, capped in length.
func sanitizeDiagnostic(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if len(s) > maxDiagnostic {
		s = strings.ToValidUTF8(s[:maxDiagnostic], "") + "…"
	}
	return s
}

// MermaidReport summarizes a wiki-wide mermaid pass.
type MermaidReport struct {
	FilesScanned, FencesChecked, FencesDegraded int
	RepairedFiles                               []string
}

// ValidateMermaid degrades invalid diagrams in every concept page.
func (w Wiki) ValidateMermaid() (MermaidReport, error) {
	var r MermaidReport
	pages, err := w.ConceptPages()
	if err != nil {
		return r, err
	}
	for _, p := range pages {
		content, err := w.Read(p)
		if err != nil {
			return r, err
		}
		r.FilesScanned++
		r.FencesChecked += len(ExtractMermaid(content))
		out, n := DegradeMermaid(content)
		if n == 0 {
			continue
		}
		if err := w.Write(p, out); err != nil {
			return r, err
		}
		r.FencesDegraded += n
		r.RepairedFiles = append(r.RepairedFiles, p)
	}
	return r, nil
}
