// Package search ranks and reads wiki sections.
//
// Search is lexical, matching upstream OpenWiki: every query builds a
// throwaway in-memory SQLite FTS5 index over section-sized units, tokenized
// with porter stemming over unicode61, and ranked by BM25 with per-field
// weights. Results are then re-ordered by source-path hints and by how many
// query terms a unit contains. A Reranker hook allows an optional semantic
// stage later.
package search

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite with FTS5

	"owcli/internal/claims"
	"owcli/internal/okf"
)

// Limits on requests.
const (
	MaxQueryChars   = 2000
	MaxPathHints    = 20
	MaxPathChars    = 500
	MaxResults      = 20
	DefaultResults  = 5
	MaxAnchors      = 20
	MaxAnchorChars  = 500
	MaxPageChars    = 1000
	excerptChars    = 600
	maxQueryTerms   = 64
	resultKindLabel = "section"
)

// BM25 field weights, in column order: title, description, heading, prose,
// identifiers (page path, tags, and source paths).
const bm25Weights = "8, 4, 6, 1, 3"

// ErrInvalidRequest marks a request the caller should correct.
var ErrInvalidRequest = errors.New("invalid search request")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

// Request is one search.
type Request struct {
	Query string
	Paths []string // repository-relative source path hints
	Limit int      // 0 means DefaultResults
}

// Result is one ranked section.
type Result struct {
	Kind    string   `json:"kind"`
	Ref     []string `json:"ref"`     // "openwiki/<page>#<anchor>"
	Content string   `json:"content"` // title, section, description, excerpt
}

// Reranker reorders lexical candidates, e.g. with embeddings. It receives up
// to MaxResults candidates and must return a subset in its preferred order.
type Reranker interface {
	Rerank(query string, candidates []Result) ([]Result, error)
}

// Options adjusts a search.
type Options struct {
	Reranker Reranker
}

var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields("a an and are as at be by can do does for from how i in is it of on or our that the their this to was we what when where which why with your") {
		stopWords[w] = true
	}
}

// Search ranks the sections of every retrievable page in the wiki.
func Search(st *claims.Store, req Request, opts Options) ([]Result, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" || len(query) > MaxQueryChars {
		return nil, invalidf("use a non-empty search query of at most %d characters", MaxQueryChars)
	}
	limit := req.Limit
	if limit == 0 {
		limit = DefaultResults
	}
	if limit < 1 || limit > MaxResults {
		return nil, invalidf("search limit must be an integer from 1 to %d", MaxResults)
	}
	if len(req.Paths) > MaxPathHints {
		return nil, invalidf("search accepts at most %d source path hints", MaxPathHints)
	}
	var hints []string
	for _, p := range req.Paths {
		h, err := normalizePathHint(p)
		if err != nil {
			return nil, err
		}
		hints = append(hints, h)
	}
	terms := queryTerms(query)
	if len(terms) == 0 {
		return []Result{}, nil
	}

	pages, err := st.DiscoverPages()
	if err != nil {
		return nil, err
	}
	var all []unit
	for _, p := range pages {
		if !retrievable(p) {
			continue
		}
		md, err := st.ReadMarkdown(p)
		if err != nil {
			return nil, err
		}
		all = append(all, units(md, p, terms)...)
	}
	if len(all) == 0 {
		return []Result{}, nil
	}
	ranked, err := rank(all, terms, hints)
	if err != nil {
		return nil, err
	}
	n := limit
	if opts.Reranker != nil {
		n = MaxResults
	}
	results := make([]Result, 0, n)
	for _, i := range ranked {
		if len(results) == n {
			break
		}
		results = append(results, all[i].result())
	}
	if opts.Reranker != nil {
		if results, err = opts.Reranker.Rerank(query, results); err != nil {
			return nil, err
		}
		if len(results) > limit {
			results = results[:limit]
		}
	}
	return results, nil
}

// rank returns unit indexes in result order.
func rank(units []unit, terms, hints []string) ([]int, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // each connection would get its own :memory: database
	if _, err := db.Exec("CREATE VIRTUAL TABLE units USING fts5(title, description, heading, prose, identifiers, tokenize='porter unicode61')"); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	insert, err := tx.Prepare("INSERT INTO units(rowid, title, description, heading, prose, identifiers) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	for i, u := range units {
		title, desc, ids := normalizeText(u.title), normalizeText(u.description), normalizeText(u.identifiers)
		if u.introductionOnly {
			title, desc, ids = "", "", ""
		}
		if _, err := insert.Exec(i+1, title, desc, normalizeText(u.heading), normalizeText(u.prose), ids); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	type row struct {
		id    int
		score float64
	}
	var rows []row
	q, err := db.Query("SELECT rowid, bm25(units, "+bm25Weights+") FROM units WHERE units MATCH ? ORDER BY 2, rowid", strings.Join(quoted, " OR "))
	if err != nil {
		return nil, err
	}
	for q.Next() {
		var r row
		if err := q.Scan(&r.id, &r.score); err != nil {
			q.Close()
			return nil, err
		}
		rows = append(rows, r)
	}
	q.Close()

	coverage := map[int]int{}
	for _, t := range quoted {
		m, err := db.Query("SELECT rowid FROM units WHERE units MATCH ?", t)
		if err != nil {
			return nil, err
		}
		for m.Next() {
			var id int
			if err := m.Scan(&id); err != nil {
				m.Close()
				return nil, err
			}
			coverage[id]++
		}
		m.Close()
	}
	pathScore := func(id int) int { return units[id-1].pathMatches(hints) }
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if pa, pb := pathScore(a.id), pathScore(b.id); pa != pb {
			return pa > pb
		}
		if coverage[a.id] != coverage[b.id] {
			return coverage[a.id] > coverage[b.id]
		}
		if a.score != b.score {
			return a.score < b.score
		}
		return a.id < b.id
	})
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.id - 1
	}
	return out, nil
}

var (
	wordToken    = regexp.MustCompile(`[\p{L}\p{N}_]+`)
	hasAlnum     = regexp.MustCompile(`[\p{L}\p{N}]`)
	lowerToUpper = regexp.MustCompile(`([\p{Ll}\d])(\p{Lu})`)
	acronymEnd   = regexp.MustCompile(`(\p{Lu})(\p{Lu}\p{Ll})`)
	separators   = regexp.MustCompile(`[_./:#-]+`)
)

// normalizeText appends a split form of text so identifiers match their
// parts: "retryHandler src/x.go" also yields "retry Handler src x go".
func normalizeText(s string) string {
	split := lowerToUpper.ReplaceAllString(s, "$1 $2")
	split = acronymEnd.ReplaceAllString(split, "$1 $2")
	split = separators.ReplaceAllString(split, " ")
	return s + " " + split
}

// queryTerms returns up to 64 distinct lowercase words of the query, without
// stop words unless nothing else remains.
func queryTerms(query string) []string {
	seen := map[string]bool{}
	var all []string
	for _, t := range wordToken.FindAllString(strings.ToLower(normalizeText(query)), -1) {
		if !seen[t] && hasAlnum.MatchString(t) {
			seen[t] = true
			all = append(all, t)
		}
	}
	if len(all) > maxQueryTerms {
		all = all[:maxQueryTerms]
	}
	var meaningful []string
	for _, t := range all {
		if !stopWords[t] {
			meaningful = append(meaningful, t)
		}
	}
	if len(meaningful) > 0 {
		return meaningful
	}
	return all
}

func normalizePathHint(p string) (string, error) {
	n := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"), "./")
	bad := n == "" || len(n) > MaxPathChars || strings.HasPrefix(n, "/") || strings.Contains(n, ":") ||
		strings.ContainsAny(n, "*?[]")
	for _, part := range strings.Split(n, "/") {
		bad = bad || part == "" || part == "." || part == ".."
	}
	for _, c := range n {
		bad = bad || c < ' '
	}
	if bad {
		return "", invalidf("source hints must be repository-relative paths without traversal or globs")
	}
	return strings.ToLower(n), nil
}

// retrievable excludes pages with any hidden path segment.
func retrievable(page string) bool {
	for _, seg := range strings.Split(page, "/") {
		if strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return true
}

// unit is one searchable section.
type unit struct {
	ref              string
	title            string
	description      string
	heading          string
	prose            string
	identifiers      string
	sourcePaths      []string
	excerpt          string
	introductionOnly bool
}

func (u unit) result() Result {
	var parts []string
	for _, p := range []string{u.title, sectionLabel(u.heading), u.description, u.excerpt} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return Result{Kind: resultKindLabel, Ref: []string{u.ref}, Content: strings.Join(parts, "\n")}
}

func sectionLabel(h string) string {
	if h == "" {
		return ""
	}
	return "Section: " + h
}

func (u unit) pathMatches(hints []string) int {
	n := 0
	for _, h := range hints {
		for _, p := range u.sourcePaths {
			if p == h || strings.HasSuffix(p, "/"+h) || strings.HasSuffix(h, "/"+p) {
				n++
				break
			}
		}
	}
	return n
}

var navHeading = regexp.MustCompile(`(?i)^(related (pages|reading|links)|see also|navigation)$`)

// units splits one page into searchable units: one per H2 section (except
// navigation sections) plus an introduction unit for text between the H1 and
// the first H2; a page without H2 sections is one unit.
func units(md, page string, terms []string) []unit {
	fields, _ := okf.Fields(md)
	if status, _ := okf.StringField(fields, "status"); status == "deprecated" {
		return nil
	}
	body := strings.TrimSpace(okf.Body(md))
	if body == "" {
		return nil
	}
	rel := strings.TrimPrefix(page, "/")
	title, ok := okf.StringField(fields, "title")
	if !ok {
		title = rel
	}
	desc, _ := okf.StringField(fields, "description")
	var sourcePaths []string
	if list, ok := fields["sources"].([]any); ok {
		for _, s := range list {
			if m, ok := s.(map[string]any); ok {
				if r, ok := okf.StringField(m, "resource"); ok {
					sourcePaths = append(sourcePaths, pathFromResource(r))
				}
			}
		}
	}
	ids := strings.Join(append(append([]string{rel}, okf.StringList(fields, "tags")...), sourcePaths...), " ")

	src := []byte(body)
	hs := headings(src)
	secs := sections(src, hs)
	mk := func(s section, prose string, intro bool) unit {
		h := s.text
		if s.depth == 1 {
			h = ""
		}
		return unit{
			ref: rel + "#" + s.anchor, title: title, description: desc, heading: h, prose: prose,
			identifiers: ids, sourcePaths: sourcePaths, excerpt: excerpt(prose, terms), introductionOnly: intro,
		}
	}
	var out []unit
	var h1 *section
	for i := range secs {
		if secs[i].depth == 1 {
			h1 = &secs[i]
			break
		}
	}
	for _, s := range secs {
		if s.depth == 2 && !navHeading.MatchString(strings.TrimSpace(s.text)) {
			out = append(out, mk(s, s.raw, false))
		}
	}
	if len(out) > 0 {
		if h1 != nil {
			if intro := introduction(src, hs, h1.heading); strings.TrimSpace(intro) != "" {
				out = append([]unit{mk(*h1, intro, true)}, out...)
			}
		}
		return out
	}
	if h1 != nil {
		return []unit{mk(*h1, body, false)}
	}
	return nil
}

// introduction is the non-heading text after an H1 and before the first H2
// inside that H1's section.
func introduction(src []byte, hs []heading, h1 heading) string {
	var b strings.Builder
	cursor := h1.end
	for _, h := range hs {
		if h.start < h1.end {
			continue
		}
		if h.depth <= 2 {
			b.Write(src[cursor:h.start])
			return b.String()
		}
		b.Write(src[cursor:h.start])
		cursor = h.end
	}
	b.Write(src[cursor:])
	return b.String()
}

func pathFromResource(r string) string {
	r = strings.TrimPrefix(r, "repo://")
	if i := strings.IndexByte(r, '#'); i >= 0 {
		r = r[:i]
	}
	return strings.ToLower(strings.TrimPrefix(strings.ReplaceAll(r, `\`, "/"), "./"))
}

var (
	mdImage      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLink       = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdPunct      = regexp.MustCompile("[`*_>#|]")
	spaceRunsAll = regexp.MustCompile(`\s+`)
)

// excerpt picks the block containing the most query terms (earliest on ties)
// and flattens it to at most 600 characters.
func excerpt(raw string, terms []string) string {
	best, bestScore := "", -1
	for _, b := range blocks(raw) {
		norm := strings.ToLower(normalizeText(b))
		score := 0
		for _, t := range terms {
			if strings.Contains(norm, t) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = b, score
		}
	}
	if best == "" {
		return ""
	}
	s := mdImage.ReplaceAllString(best, "$1")
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdPunct.ReplaceAllString(s, " ")
	s = strings.TrimSpace(spaceRunsAll.ReplaceAllString(s, " "))
	if len([]rune(s)) <= excerptChars {
		return s
	}
	r := []rune(s)[:excerptChars+1]
	cut := strings.LastIndex(string(r), " ")
	if cut <= 0 {
		return string(r[:excerptChars]) + "…"
	}
	return string(r)[:cut] + "…"
}

// Section is one section returned by Read.
type Section struct {
	Anchor  string `json:"section"`
	Content string `json:"content"`
}

// Read returns complete sections (heading through the end of its subtree) of
// one page by anchor, in the requested order.
func Read(st *claims.Store, page string, anchors []string) (string, []Section, error) {
	if len(anchors) == 0 {
		return "", nil, invalidf("provide at least one section anchor")
	}
	if len(anchors) > MaxAnchors {
		return "", nil, invalidf("read accepts at most %d section anchors", MaxAnchors)
	}
	if len(strings.TrimSpace(page)) > MaxPageChars {
		return "", nil, invalidf("page must be a non-structural Markdown path below openwiki/")
	}
	norm, err := claims.NormalizeToolPage(page)
	if err != nil || !retrievable(norm) {
		return "", nil, invalidf("page must be a non-structural Markdown path below openwiki/")
	}
	var wanted []string
	for _, a := range anchors {
		a = strings.TrimPrefix(strings.TrimSpace(a), "#")
		if a == "" || len(a) > MaxAnchorChars || strings.ContainsAny(a, "#/") || hasControl(a) {
			return "", nil, invalidf("sections must be heading anchors without a page path")
		}
		wanted = append(wanted, a)
	}
	md, err := st.ReadMarkdown(norm)
	if err != nil {
		return "", nil, err
	}
	src := []byte(okf.Body(md))
	available := map[string]string{}
	for _, s := range sections(src, headings(src)) {
		available[s.anchor] = s.raw
	}
	var missing []string
	out := make([]Section, 0, len(wanted))
	for _, a := range wanted {
		raw, ok := available[a]
		if !ok {
			missing = append(missing, a)
			continue
		}
		out = append(out, Section{Anchor: a, Content: raw})
	}
	if len(missing) > 0 {
		return "", nil, invalidf("unknown section(s): %s", strings.Join(missing, ", "))
	}
	return strings.TrimPrefix(norm, "/"), out, nil
}

func hasControl(s string) bool {
	for _, c := range s {
		if c < ' ' {
			return true
		}
	}
	return false
}
