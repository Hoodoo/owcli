package search

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hoodoo/owcli/internal/claims"
	"github.com/Hoodoo/owcli/internal/store"
)

// upstreamScript calls upstream's searchWiki for each query and prints the
// ranked refs as JSON.
const upstreamScript = `
const m = await import(process.argv[1] + '/dist/retrieval/wiki.js');
const out = [];
for (const q of JSON.parse(process.argv[3])) {
  const r = await m.searchWiki(process.argv[2], { query: q.query, paths: q.paths, limit: 10 });
  out.push(r.results.map((x) => x.ref[0] + '\u0000' + x.content));
}
console.log(JSON.stringify(out));
`

type compatQuery struct {
	Query string   `json:"query"`
	Paths []string `json:"paths,omitempty"`
}

var compatQueries = []compatQuery{
	{Query: "how does retry handling work"},
	{Query: "grounded claims evidence staleness"},
	{Query: "sparse reconciliation confirmedClaimIds"},
	{Query: "openwikiignore"},
	{Query: "What is the page manifest?"},
	{Query: "mermaid diagram validation"},
	{Query: "how to run the tests"},
	{Query: "search ranking bm25 FTS5"},
	{Query: "visualizer graph export"},
	{Query: "workspace linking"},
	{Query: "provider configuration API key"},
	{Query: "skipped pages resume"},
	{Query: "the"},
	{Query: "index synchronization", Paths: []string{"src/okf/index-sync.ts"}},
	{Query: "claims", Paths: []string{"session.ts", "src/generation/page-jobs.ts"}},
	{Query: "RepositoryEvidenceResolver line range relocation"},
	{Query: "cron schedule GitHub Actions workflow"},
	{Query: "telemetry opt out"},
}

// TestUpstreamSearchParity compares ranked results (refs and content) with upstream's own
// implementation. Set OWCLI_UPSTREAM_DIR (a checkout with openwiki/) and
// OWCLI_UPSTREAM_PKG (an installed openwiki npm package; needs node).
func TestUpstreamSearchParity(t *testing.T) {
	dir, pkg := os.Getenv("OWCLI_UPSTREAM_DIR"), os.Getenv("OWCLI_UPSTREAM_PKG")
	if dir == "" || pkg == "" {
		t.Skip("OWCLI_UPSTREAM_DIR and OWCLI_UPSTREAM_PKG not set (the operator installs upstream with make openwiki-install)")
	}
	dir, _ = filepath.Abs(dir)
	qs, _ := json.Marshal(compatQueries)
	cmd := exec.Command("node", "--input-type=module", "-e", upstreamScript, "--", pkg, dir, string(qs))
	cmd.Env = append(os.Environ(), "OPENWIKI_CONFIG_DIR="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("upstream search: %v", err)
	}
	var want [][]string
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	st := claims.NewStore(store.Layout{WikiRoot: filepath.Join(dir, "openwiki")})
	same := 0
	for i, q := range compatQueries {
		rs, err := Search(st, Request{Query: q.Query, Paths: q.Paths, Limit: 10}, Options{})
		if err != nil {
			t.Fatalf("%q: %v", q.Query, err)
		}
		var got []string
		for _, r := range rs {
			got = append(got, r.Ref[0]+"\x00"+r.Content)
		}
		if strings.Join(got, "\n") == strings.Join(want[i], "\n") {
			same++
			continue
		}
		for j := range got {
			if j >= len(want[i]) || got[j] != want[i][j] {
				w := "<none>"
				if j < len(want[i]) {
					w = want[i][j]
				}
				t.Errorf("%q result %d differs:\n  owcli:    %q\n  upstream: %q", q.Query, j, got[j], w)
				break
			}
		}
		if len(got) != len(want[i]) {
			t.Errorf("%q: %d results, upstream %d", q.Query, len(got), len(want[i]))
		}
	}
	t.Logf("%d/%d queries rank identically", same, len(compatQueries))
}

// upstreamWorkspaceScript searches from the repository in argv[2] with
// upstream's workspace resolution and prints wiki, ref, and content.
const upstreamWorkspaceScript = `
const m = await import(process.argv[1] + '/dist/retrieval/wiki.js');
const out = [];
for (const q of JSON.parse(process.argv[3])) {
  const r = await m.searchWiki(process.argv[2], { query: q.query, paths: q.paths, limit: 10 });
  if (r.status === 'workspace_required') throw new Error('workspace_required');
  out.push(r.results.map((x) => x.wiki + '\u0000' + x.ref[0]));
}
console.log(JSON.stringify(out));
`

// TestUpstreamWorkspaceSearchParity splits owcli's own wiki across two
// repositories in one workspace, registers it identically for both tools, and
// compares federated rankings. Set OWCLI_UPSTREAM_PKG (needs node and git).
func TestUpstreamWorkspaceSearchParity(t *testing.T) {
	pkg := os.Getenv("OWCLI_UPSTREAM_PKG")
	if pkg == "" {
		t.Skip("OWCLI_UPSTREAM_PKG not set (the operator installs upstream with make openwiki-install)")
	}
	source, err := filepath.Abs(filepath.Join("..", "..", "openwiki"))
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	roots := map[string]string{"alpha": filepath.Join(base, "alpha"), "beta": filepath.Join(base, "beta")}
	err = filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		rel, _ := filepath.Rel(source, path)
		owner := "beta"
		if strings.HasPrefix(rel, "architecture") || strings.HasPrefix(rel, "concepts") {
			owner = "alpha"
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(roots[owner], "openwiki", rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	lastUpdate, err := os.ReadFile(filepath.Join(source, ".last-update.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, root := range roots {
		if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		if err := os.WriteFile(filepath.Join(root, "openwiki", ".last-update.json"), lastUpdate, 0o644); err != nil {
			t.Fatal(err)
		}
		if roots[name], err = filepath.EvalSymlinks(root); err != nil {
			t.Fatal(err)
		}
	}

	registry, _ := json.Marshal(map[string]any{
		"version": 1,
		"wikis": []map[string]string{
			{"id": "alpha", "name": "alpha", "root": roots["alpha"]},
			{"id": "beta", "name": "beta", "root": roots["beta"]},
		},
		"workspaces": []map[string]any{{"id": "split", "name": "Split", "wikis": []string{"alpha", "beta"}}},
		"active":     []any{},
	})
	upstreamHome := filepath.Join(base, "upstream-home")
	dirs := store.Dirs{Config: filepath.Join(base, "config"), Data: filepath.Join(base, "data")}
	for _, path := range []string{filepath.Join(upstreamHome, "wiki-workspaces.json"), filepath.Join(dirs.Config, "workspaces.json")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, registry, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	qs, _ := json.Marshal(compatQueries)
	cmd := exec.Command("node", "--input-type=module", "-e", upstreamWorkspaceScript, "--", pkg, roots["alpha"], string(qs))
	cmd.Env = append(os.Environ(), "OPENWIKI_CONFIG_DIR="+upstreamHome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("upstream search: %v\n%s", err, out)
	}
	var want [][]string
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}

	scope, err := dirs.ResolveSearchScope(roots["alpha"], "")
	if err != nil || scope.Workspace == nil || len(scope.Wikis) != 2 {
		t.Fatalf("owcli scope: %+v %v", scope, err)
	}
	var sources []Source
	for _, w := range scope.Wikis {
		sources = append(sources, Source{Store: claims.NewStore(w.Layout), Wiki: w.ID})
	}
	same := 0
	for i, q := range compatQueries {
		rs, err := SearchWikis(sources, Request{Query: q.Query, Paths: q.Paths, Limit: 10}, Options{})
		if err != nil {
			t.Fatalf("%q: %v", q.Query, err)
		}
		var got []string
		for _, r := range rs {
			got = append(got, r.Wiki+"\x00"+r.Ref[0])
		}
		if strings.Join(got, "\n") == strings.Join(want[i], "\n") {
			same++
			continue
		}
		for j := 0; j < len(got) || j < len(want[i]); j++ {
			g, w := "<none>", "<none>"
			if j < len(got) {
				g = got[j]
			}
			if j < len(want[i]) {
				w = want[i][j]
			}
			if g != w {
				t.Errorf("%q result %d differs:\n  owcli:    %q\n  upstream: %q", q.Query, j, g, w)
				break
			}
		}
	}
	t.Logf("%d/%d workspace queries rank identically", same, len(compatQueries))
}
