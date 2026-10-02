package serve

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owcli/internal/store"
)

const pageA = "---\ntype: Concept\ntitle: Alpha\ndescription: First page.\ntags: [one]\n---\n\n# Alpha\n\nSee [Beta](b.md#details) and ![img](b.md).\n\n## Retry policy\n\nAlpha retries with backoff.\n\n<script>alert(1)</script>\n"
const pageB = "---\ntype: Guide\ntitle: Beta\ndescription: Second page.\n---\n\n# Beta\n\n## Details\n\nBack to [Alpha](a.md).\n\n```mermaid\nflowchart LR\n  A --> B\n```\n"

// fixture builds two Git repositories with wikis, registered in one
// workspace, and a server whose default scope is the first wiki.
func fixture(t *testing.T) *httptest.Server {
	t.Helper()
	base := t.TempDir()
	dirs := store.Dirs{Config: filepath.Join(base, "config"), Data: filepath.Join(base, "data")}
	mk := func(name string, pages map[string]string) string {
		root := filepath.Join(base, name)
		for rel, content := range pages {
			p := filepath.Join(root, "openwiki", rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		real, _ := filepath.EvalSymlinks(root)
		return real
	}
	one := mk("one", map[string]string{"concepts/a.md": pageA, "concepts/b.md": pageB})
	two := mk("two", map[string]string{"c.md": "---\ntype: Concept\ntitle: Gamma\n---\n\n# Gamma\n\nGamma also retries.\n"})
	if _, err := dirs.SaveWorkspaces([]store.WorkspaceDraft{{Name: "Pair", Roots: []string{one, two}}}); err != nil {
		t.Fatal(err)
	}
	def, err := dirs.ResolveWikiRef("one")
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Dirs: dirs, Default: []store.ScopedWiki{def}, Version: "test", Wikis: func() (any, error) { return map[string]any{"wikis": []string{"one", "two"}}, nil }})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, srv *httptest.Server, path string, want int) map[string]any {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s: status %d, want %d: %v", path, resp.StatusCode, want, v)
	}
	return v
}

func TestGraph(t *testing.T) {
	srv := fixture(t)
	g := get(t, srv, "/api/graph", 200)
	nodes := g["nodes"].([]any)
	if len(nodes) != 2 || len(g["edges"].([]any)) != 2 {
		t.Fatalf("default graph: %v", g)
	}
	a := nodes[0].(map[string]any)
	if a["id"] != "one:concepts/a" || a["title"] != "Alpha" || a["type"] != "Concept" || a["page"] != "concepts/a.md" {
		t.Fatalf("node: %v", a)
	}
	if links := a["links"].([]any); len(links) != 1 || links[0] != "one:concepts/b" {
		t.Errorf("links (image excluded): %v", links)
	}
	if bl := a["backlinks"].([]any); len(bl) != 1 || bl[0] != "one:concepts/b" {
		t.Errorf("backlinks: %v", bl)
	}

	g = get(t, srv, "/api/graph?workspace=pair", 200)
	if len(g["nodes"].([]any)) != 3 || g["workspace"].(map[string]any)["id"] != "pair" || len(g["wikis"].([]any)) != 2 {
		t.Fatalf("workspace graph: %v", g)
	}
	if g := get(t, srv, "/api/graph?wiki=two", 200); g["nodes"].([]any)[0].(map[string]any)["id"] != "two:c" {
		t.Fatalf("wiki graph: %v", g)
	}
	get(t, srv, "/api/graph?wiki=one&workspace=pair", 400)
	get(t, srv, "/api/graph?workspace=nope", 400)
}

func TestPage(t *testing.T) {
	srv := fixture(t)
	p := get(t, srv, "/api/page?wiki=one&page=concepts/a.md", 200)
	html := p["html"].(string)
	if p["title"] != "Alpha" || !strings.Contains(html, `<h2 id="retry-policy">`) || !strings.Contains(html, `href="b.md#details"`) {
		t.Fatalf("page: %v", p)
	}
	if strings.Contains(html, "<script>") {
		t.Fatal("raw HTML from a page must not be passed through")
	}
	if p := get(t, srv, "/api/page?wiki=one&page=openwiki/concepts/b.md", 200); !strings.Contains(p["html"].(string), `class="language-mermaid"`) {
		t.Fatalf("mermaid block: %v", p["html"])
	}
	get(t, srv, "/api/page?wiki=one&page=../../../etc/passwd", 404)
	get(t, srv, "/api/page?wiki=one&page=concepts/missing.md", 404)
	get(t, srv, "/api/page?wiki=nope&page=a.md", 400)
	get(t, srv, "/api/page?wiki=one", 400)
}

func TestSearchConfigWikisAndReadOnly(t *testing.T) {
	srv := fixture(t)
	r := get(t, srv, "/api/search?q=retries&workspace=pair&limit=5", 200)
	wikis := map[string]bool{}
	for _, x := range r["results"].([]any) {
		wikis[x.(map[string]any)["wiki"].(string)] = true
	}
	if !wikis["one"] || !wikis["two"] {
		t.Fatalf("workspace search: %v", r)
	}
	if r := get(t, srv, "/api/search?q=retries", 200); len(r["results"].([]any)) != 1 {
		t.Fatalf("default-scope search: %v", r)
	}
	get(t, srv, "/api/search?q=", 400)
	if c := get(t, srv, "/api/config", 200); c["default"].(map[string]any)["wikis"].([]any)[0] != "one" {
		t.Fatalf("config: %v", c)
	}
	get(t, srv, "/api/wikis", 200)
	resp, err := http.Post(srv.URL+"/api/wikis", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST must be refused: %v", err)
	}
	if resp, err := http.Get(srv.URL + "/"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("index: %v", err)
	}
}

func TestListenIsLoopbackAndSkipsBusyPorts(t *testing.T) {
	first, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	busy := first.Addr().(*net.TCPAddr)
	if !busy.IP.IsLoopback() {
		t.Fatalf("not loopback: %s", busy)
	}
	next, err := Listen(busy.Port)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if got := next.Addr().(*net.TCPAddr); got.Port == busy.Port || !got.IP.IsLoopback() {
		t.Fatalf("a busy port must be skipped: %s", got)
	}
}

func TestUIFilesAreServed(t *testing.T) {
	srv := fixture(t)
	for path, want := range map[string]string{
		"/":          `<nav id="sidebar"`,
		"/app.js":    `api("/api/graph"`,
		"/style.css": `#graph`,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		_, _ = io.Copy(&b, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(b.String(), want) {
			t.Errorf("%s: status %d, missing %q", path, resp.StatusCode, want)
		}
	}
}
