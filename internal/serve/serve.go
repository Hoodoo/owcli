// Package serve is owcli's read-only browser viewer: a loopback HTTP server
// with a JSON API over the wikis owcli knows (graph, rendered pages with
// their Claims, search) and an embedded single-page UI.
//
// Every request reads the wiki files fresh, so edits show up on reload.
package serve

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"

	"github.com/Hoodoo/owcli/internal/claims"
	"github.com/Hoodoo/owcli/internal/evidence"
	"github.com/Hoodoo/owcli/internal/ignore"
	"github.com/Hoodoo/owcli/internal/okf"
	"github.com/Hoodoo/owcli/internal/search"
	"github.com/Hoodoo/owcli/internal/store"
)

//go:embed static
var staticFiles embed.FS

// Options configure a Server.
type Options struct {
	Dirs store.Dirs
	// Default is the scope serve started in (the current repository's wiki or
	// workspace); requests without a wiki or workspace use it. It may be
	// empty, and its wikis need not be registered.
	Default          []store.ScopedWiki
	DefaultWorkspace *store.WorkspaceSummary
	// Wikis returns the `owcli wikis` listing.
	Wikis func() (any, error)
	// Version is shown by the UI.
	Version string

	// ListenHost is the host part of the listen address. Requests must name
	// it, localhost, a loopback IP, or one of AllowHosts in their Host
	// header, which keeps other web pages from reading the wikis by DNS
	// rebinding.
	ListenHost string
	// AllowHosts are further names accepted in the Host header, such as the
	// public name a reverse proxy forwards.
	AllowHosts []string
	// UserHeader names a request header a proxy sets to the signed-in viewer
	// (X-Goog-Authenticated-User-Email behind Google IAP). When set,
	// requests without it are refused, so traffic that bypasses the proxy
	// fails closed. Only set it when nothing but the proxy can reach the
	// server.
	UserHeader string
}

// Server answers viewer requests.
type Server struct{ opts Options }

// New returns a Server.
func New(o Options) *Server { return &Server{opts: o} }

// Handler routes the API and the embedded UI.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", s.get(s.config))
	mux.HandleFunc("/api/wikis", s.get(func(*http.Request) (any, error) { return s.opts.Wikis() }))
	mux.HandleFunc("/api/graph", s.get(s.graph))
	mux.HandleFunc("/api/page", s.get(s.page))
	mux.HandleFunc("/api/search", s.get(s.search))
	sub, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if s.opts.UserHeader != "" && s.Viewer(r) == "" {
			http.Error(w, "no signed-in user", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || host == strings.ToLower(s.opts.ListenHost) {
		return true
	}
	for _, h := range s.opts.AllowHosts {
		if host == strings.ToLower(h) {
			return true
		}
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Viewer returns who the proxy says is signed in, or "" without a
// UserHeader. Google IAP prefixes the address with "accounts.google.com:".
func (s *Server) Viewer(r *http.Request) string {
	if s.opts.UserHeader == "" {
		return ""
	}
	v := strings.TrimSpace(r.Header.Get(s.opts.UserHeader))
	if i := strings.LastIndex(v, ":"); i >= 0 {
		v = v[i+1:]
	}
	return v
}

// requestError is a client mistake, answered with 400 or 404.
type requestError struct {
	status int
	msg    string
}

func (e *requestError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &requestError{status: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

func (s *Server) get(fn func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		v, err := fn(r)
		status := http.StatusOK
		if err != nil {
			status = http.StatusInternalServerError
			var re *requestError
			switch {
			case errors.As(err, &re):
				status = re.status
			case errors.Is(err, store.ErrWorkspace), errors.Is(err, store.ErrUnbound), errors.Is(err, search.ErrInvalidRequest):
				status = http.StatusBadRequest
			}
			v = map[string]any{"error": map[string]string{"message": err.Error()}}
		}
		data, _ := store.MarshalJSON(v)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = w.Write(data)
	}
}

func (s *Server) config(*http.Request) (any, error) {
	var ids []string
	for _, w := range s.opts.Default {
		ids = append(ids, w.ID)
	}
	return map[string]any{"version": s.opts.Version, "default": map[string]any{"workspace": s.opts.DefaultWorkspace, "wikis": ids}}, nil
}

// scope resolves the wiki= or workspace= parameter, or the default scope.
func (s *Server) scope(r *http.Request) ([]store.ScopedWiki, *store.WorkspaceSummary, []store.WorkspaceMember, error) {
	q := r.URL.Query()
	wiki, ws := q.Get("wiki"), q.Get("workspace")
	switch {
	case wiki != "" && ws != "":
		return nil, nil, nil, badRequest("give wiki or workspace, not both")
	case wiki != "":
		w, err := s.wiki(wiki)
		return []store.ScopedWiki{w}, nil, nil, err
	case ws != "":
		reg, err := s.opts.Dirs.LoadWorkspaces()
		if err != nil {
			return nil, nil, nil, err
		}
		found, err := reg.FindWorkspace(ws)
		if err != nil {
			return nil, nil, nil, err
		}
		members, err := s.opts.Dirs.ResolveMembers(reg, found)
		if err != nil {
			return nil, nil, nil, err
		}
		var wikis []store.ScopedWiki
		var skipped []store.WorkspaceMember
		for _, m := range members {
			if m.Problem != "" {
				skipped = append(skipped, m)
				continue
			}
			wikis = append(wikis, store.ScopedWiki{WikiIdentity: store.WikiIdentity{ID: m.Wiki.ID, Name: m.Wiki.Name}, Layout: m.Layout})
		}
		sum := store.WorkspaceSummary{ID: found.ID, Name: found.Name, WikiCount: len(found.Wikis)}
		return wikis, &sum, skipped, nil
	case len(s.opts.Default) > 0:
		return s.opts.Default, s.opts.DefaultWorkspace, nil, nil
	}
	return nil, nil, nil, badRequest("choose a wiki or a workspace")
}

// wiki resolves a wiki ID: one of the default scope's, else from the
// registries.
func (s *Server) wiki(id string) (store.ScopedWiki, error) {
	for _, w := range s.opts.Default {
		if w.ID == id {
			return w, nil
		}
	}
	return s.opts.Dirs.ResolveWikiRef(id)
}

// pageClaims is a page's Claims and their preflight issues.
type pageClaims struct {
	claims     []claims.Claim
	issues     map[string]claims.Issue // by claim ID
	stale      int
	unresolved int
}

// preflight checks every Claim of a wiki against current source.
func preflight(l store.Layout) (map[string]*pageClaims, error) {
	m, err := ignore.Load(l.RepoRoot)
	if err != nil {
		return nil, err
	}
	resolver, err := evidence.NewRepoResolver(l.RepoRoot, m.Exclude(l.RepoExclusions()...))
	if err != nil {
		return nil, err
	}
	pf, err := claims.RunPreflight(claims.NewStore(l), resolver)
	if err != nil {
		return nil, err
	}
	out := map[string]*pageClaims{}
	at := func(page string) *pageClaims {
		if out[page] == nil {
			out[page] = &pageClaims{issues: map[string]claims.Issue{}}
		}
		return out[page]
	}
	for page, pc := range pf.Persisted {
		at(page).claims = pc.Claims
	}
	for _, is := range pf.Issues {
		p := at(is.Page)
		p.issues[is.ClaimID] = is
		if is.Kind == claims.Stale {
			p.stale++
		} else {
			p.unresolved++
		}
	}
	return out, nil
}

// pageInfo is a concept page's front matter, body, and outgoing links.
type pageInfo struct {
	id, rel, title, typ, description string
	tags                             []string
	body                             string
	links                            []string
}

// readPages reads every concept page of a wiki with its links.
func readPages(l store.Layout) ([]pageInfo, error) {
	w := okf.ForLayout(l)
	ids, err := w.ConceptPages()
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, id := range ids {
		known[id] = true
	}
	var out []pageInfo
	for _, id := range ids {
		content, err := w.Read(id)
		if err != nil {
			return nil, err
		}
		rel, _ := store.PageRel(id)
		fields, _ := okf.Fields(content)
		body := okf.Body(content)
		p := pageInfo{id: id, rel: rel, body: body, links: okf.PageLinks(id, content, known)}
		var ok bool
		if p.title, ok = okf.StringField(fields, "title"); !ok || p.title == "" {
			p.title = okf.DeriveTitle(body, rel)
		}
		if p.typ, ok = okf.StringField(fields, "type"); !ok || p.typ == "" {
			p.typ = "Reference"
		}
		p.description, _ = okf.StringField(fields, "description")
		p.tags = okf.StringList(fields, "tags")
		out = append(out, p)
	}
	return out, nil
}

// nodeID is a graph node's identity: the wiki ID and the page path without
// .md, so pages of different wikis never collide.
func nodeID(wiki, rel string) string { return wiki + ":" + strings.TrimSuffix(rel, ".md") }

type graphNode struct {
	ID          string   `json:"id"`
	Wiki        string   `json:"wiki"`
	Page        string   `json:"page"` // wiki-relative path, for /api/page
	Title       string   `json:"title"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Size        int      `json:"size"` // body length in characters
	Links       []string `json:"links"`
	Backlinks   []string `json:"backlinks"`
	Claims      int      `json:"claims"`
	Stale       int      `json:"stale"`
	Unresolved  int      `json:"unresolved"`
}

type graphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

func (s *Server) graph(r *http.Request) (any, error) {
	wikis, ws, skipped, err := s.scope(r)
	if err != nil {
		return nil, err
	}
	nodes := []*graphNode{}
	edges := []graphEdge{}
	byID := map[string]*graphNode{}
	idents := []store.WikiIdentity{}
	for _, w := range wikis {
		idents = append(idents, w.WikiIdentity)
		pages, err := readPages(w.Layout)
		if err != nil {
			return nil, err
		}
		pcs, err := preflight(w.Layout)
		if err != nil {
			return nil, err
		}
		for _, p := range pages {
			n := &graphNode{ID: nodeID(w.ID, p.rel), Wiki: w.ID, Page: p.rel, Title: p.title, Type: p.typ, Description: p.description, Tags: p.tags, Size: len(p.body), Links: []string{}, Backlinks: []string{}}
			if n.Tags == nil {
				n.Tags = []string{}
			}
			if pc := pcs[p.id]; pc != nil {
				n.Claims, n.Stale, n.Unresolved = len(pc.claims), pc.stale, pc.unresolved
			}
			for _, target := range p.links {
				rel, _ := store.PageRel(target)
				n.Links = append(n.Links, nodeID(w.ID, rel))
			}
			nodes = append(nodes, n)
			byID[n.ID] = n
		}
	}
	for _, n := range nodes {
		for _, t := range n.Links {
			edges = append(edges, graphEdge{Source: n.ID, Target: t})
			byID[t].Backlinks = append(byID[t].Backlinks, n.ID)
		}
	}
	return map[string]any{"workspace": ws, "wikis": idents, "skipped": skippedOrEmpty(skipped), "nodes": nodes, "edges": edges}, nil
}

func skippedOrEmpty(m []store.WorkspaceMember) []store.WorkspaceMember {
	if m == nil {
		return []store.WorkspaceMember{}
	}
	return m
}

type claimView struct {
	ID        string           `json:"id"`
	Statement string           `json:"statement"`
	Evidence  []string         `json:"evidence"`
	Issue     *claims.IssueRef `json:"issue,omitempty"`
}

func (s *Server) page(r *http.Request) (any, error) {
	q := r.URL.Query()
	if q.Get("wiki") == "" || q.Get("page") == "" {
		return nil, badRequest("give wiki and page")
	}
	w, err := s.wiki(q.Get("wiki"))
	if err != nil {
		return nil, err
	}
	rel := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(q.Get("page"), store.WikiDirName+"/")), "/")
	id := store.PageID(rel)
	pages, err := readPages(w.Layout)
	if err != nil {
		return nil, err
	}
	var p *pageInfo
	var backlinks []string
	for i := range pages {
		if pages[i].id == id {
			p = &pages[i]
		}
		for _, l := range pages[i].links {
			if l == id {
				backlinks = append(backlinks, nodeID(w.ID, pages[i].rel))
			}
		}
	}
	if p == nil {
		return nil, &requestError{status: http.StatusNotFound, msg: fmt.Sprintf("wiki %s has no page %s", w.ID, rel)}
	}
	html, err := render(p.body)
	if err != nil {
		return nil, err
	}
	pcs, err := preflight(w.Layout)
	if err != nil {
		return nil, err
	}
	views := []claimView{}
	if pc := pcs[id]; pc != nil {
		for _, c := range pc.claims {
			v := claimView{ID: c.ID, Statement: c.Statement, Evidence: []string{}}
			for _, e := range c.Evidence {
				v.Evidence = append(v.Evidence, e.Resource)
			}
			if is, ok := pc.issues[c.ID]; ok {
				v.Issue = &claims.IssueRef{Kind: is.Kind, Resources: is.Resources}
			}
			views = append(views, v)
		}
	}
	links := []string{}
	for _, l := range p.links {
		lr, _ := store.PageRel(l)
		links = append(links, nodeID(w.ID, lr))
	}
	if backlinks == nil {
		backlinks = []string{}
	}
	tags := p.tags
	if tags == nil {
		tags = []string{}
	}
	return map[string]any{
		"wiki": w.ID, "page": rel, "id": nodeID(w.ID, rel), "title": p.title, "type": p.typ,
		"description": p.description, "tags": tags, "html": html, "claims": views,
		"links": links, "backlinks": backlinks,
	}, nil
}

// slugIDs gives headings GitHub-style anchors (okf.Slug with -N suffixes),
// the same anchors wiki links and search refs use.
type slugIDs struct{ used map[string]bool }

func (s *slugIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	base := okf.Slug(string(value))
	id := base
	for n := 1; s.used[id]; n++ {
		id = base + "-" + strconv.Itoa(n)
	}
	s.used[id] = true
	return []byte(id)
}

func (s *slugIDs) Put(value []byte) { s.used[string(value)] = true }

var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

// render converts a page body to HTML. Raw HTML in the page is not passed
// through (goldmark's default), so a page cannot inject markup.
func render(body string) (string, error) {
	var buf bytes.Buffer
	ctx := parser.NewContext(parser.WithIDs(&slugIDs{used: map[string]bool{}}))
	if err := markdown.Convert([]byte(body), &buf, parser.WithContext(ctx)); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (s *Server) search(r *http.Request) (any, error) {
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, badRequest("limit must be a number")
		}
		limit = n
	}
	wikis, ws, skipped, err := s.scope(r)
	if err != nil {
		return nil, err
	}
	var sources []search.Source
	idents := []store.WikiIdentity{}
	for _, w := range wikis {
		sources = append(sources, search.Source{Store: claims.NewStore(w.Layout), Wiki: w.ID})
		idents = append(idents, w.WikiIdentity)
	}
	results, err := search.SearchWikis(sources, search.Request{Query: q.Get("q"), Limit: limit}, search.Options{})
	if err != nil {
		return nil, err
	}
	return map[string]any{"results": results, "workspace": ws, "wikis": idents, "skipped": skippedOrEmpty(skipped)}, nil
}
