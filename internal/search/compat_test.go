package search

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"owcli/internal/claims"
	"owcli/internal/store"
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
		t.Skip("OWCLI_UPSTREAM_DIR and OWCLI_UPSTREAM_PKG not set")
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
