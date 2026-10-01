package okf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func newWiki(t *testing.T, files map[string]string) Wiki {
	t.Helper()
	base := t.TempDir()
	w := Wiki{Root: filepath.Join(base, "wiki", "openwiki"), Repo: filepath.Join(base, "repo")}
	writeTree(t, w.Root, files)
	if err := os.MkdirAll(w.Repo, 0o755); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestConceptPagesSkipsReservedAndHidden(t *testing.T) {
	w := newWiki(t, map[string]string{
		"index.md": "", "log.md": "", "INSTRUCTIONS.md": "", "a.md": "", "notes.txt": "",
		".claims/a.json": "", ".hidden/x.md": "", "sub/b.MD": "", "sub/index.md": "",
	})
	got, err := w.ConceptPages()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "/openwiki/a.md,/openwiki/sub/b.MD" {
		t.Fatalf("got %v", got)
	}
	empty := Wiki{Root: filepath.Join(t.TempDir(), "missing")}
	if pages, err := empty.ConceptPages(); err != nil || pages != nil {
		t.Fatalf("missing root: %v %v", pages, err)
	}
	if _, err := empty.SyncIndexes(""); err != nil {
		t.Fatalf("missing root index sync: %v", err)
	}
}

func TestSyncIndexes(t *testing.T) {
	w := newWiki(t, map[string]string{
		"quickstart.md":        "---\ntype: guide\ntitle: Quick [Start]\ndescription: Begin here.\n---\n# Q\n",
		"Zeta.md":              "---\ntype: concept\n---\n# Z\n",
		"concepts/alpha.md":    "no front matter\n",
		"concepts/deep/x y.md": "---\ntype: t\ntitle: X Y\n---\n",
		"empty/.keep":          "",
	})
	report, err := w.SyncIndexes("")
	if err != nil {
		t.Fatal(err)
	}
	root := readFile(t, filepath.Join(w.Root, "index.md"))
	wantRoot := "---\nokf_version: \"0.2\"\n---\n\n# Files\n\n- [Quick \\[Start\\]](quickstart.md) - Begin here.\n- [Zeta](Zeta.md)\n\n# Directories\n\n- [concepts](concepts/)\n- [empty](empty/)\n"
	if root != wantRoot {
		t.Errorf("root index:\n%q\nwant\n%q", root, wantRoot)
	}
	concepts := readFile(t, filepath.Join(w.Root, "concepts", "index.md"))
	if concepts != "# Files\n\n- [Alpha](alpha.md)\n\n# Directories\n\n- [deep](deep/)\n" {
		t.Errorf("concepts index:\n%q", concepts)
	}
	if deep := readFile(t, filepath.Join(w.Root, "concepts", "deep", "index.md")); deep != "# Files\n\n- [X Y](x%20y.md)\n" {
		t.Errorf("deep index:\n%q", deep)
	}
	if empty := readFile(t, filepath.Join(w.Root, "empty", "index.md")); empty != "# Files\n" {
		t.Errorf("empty index: %q", empty)
	}
	// alpha.md was repaired in place during indexing.
	if Validate(readFile(t, filepath.Join(w.Root, "concepts", "alpha.md"))) != nil {
		t.Error("concept should be normalized")
	}
	if strings.Join(report.GeneratedPages, ",") != "concepts/alpha.md" {
		t.Errorf("generated report %v", report.GeneratedPages)
	}
	if strings.Join(report.MissingDescriptionPages, ",") != "Zeta.md,concepts/alpha.md,concepts/deep/x y.md" {
		t.Errorf("missing description report %v", report.MissingDescriptionPages)
	}

	// Unchanged indexes are not rewritten.
	info, _ := os.Stat(filepath.Join(w.Root, "index.md"))
	if _, err := w.SyncIndexes(""); err != nil {
		t.Fatal(err)
	}
	again, _ := os.Stat(filepath.Join(w.Root, "index.md"))
	if !again.ModTime().Equal(info.ModTime()) {
		t.Error("unchanged index was rewritten")
	}
}

func TestProvenance(t *testing.T) {
	w := newWiki(t, map[string]string{
		"same.md":      "---\ntype: t\ngenerated: { by: \"owcli/0.1\", at: \"2026-01-01T00:00:00.000Z\" }\n---\nbody\n",
		"changed.md":   "---\ntype: t\ntimestamp: \"2020\"\n---\nold body\n",
		"unstamped.md": "---\ntype: t\n---\nplain\n",
	})
	snap, err := w.SnapshotProvenance()
	if err != nil {
		t.Fatal(err)
	}
	// The agent rewrites one body, drops the stamp on another, adds a page,
	// and only touches front matter of the unstamped page.
	writeTree(t, w.Root, map[string]string{
		"same.md":      "---\ntype: t\ntitle: New title\n---\nbody\n",
		"changed.md":   "---\ntype: t\ntimestamp: \"2020\"\n---\nnew body\n\n\n",
		"new.md":       "---\ntype: t\n---\nfresh",
		"unstamped.md": "---\ntype: t\ntitle: T\n---\nplain\n",
	})
	now := "2026-10-01T00:00:00.000Z"
	if err := w.FinalizeProvenance(snap, now, "owcli/1.0", map[string]string{"/openwiki/new.md": "host/agent"}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(w.Root, "same.md")); got != "---\ntype: t\ntitle: New title\ngenerated: { by: \"owcli/0.1\", at: \"2026-01-01T00:00:00.000Z\" }\n---\nbody\n" {
		t.Errorf("unchanged body should restore the prior stamp:\n%s", got)
	}
	if got := readFile(t, filepath.Join(w.Root, "changed.md")); got != "---\ntype: t\ngenerated: { by: \"owcli/1.0\", at: \""+now+"\" }\n---\nnew body\n" {
		t.Errorf("changed body:\n%q", got)
	}
	if got := readFile(t, filepath.Join(w.Root, "new.md")); !strings.Contains(got, `generated: { by: "host/agent", at: "`+now+`" }`) {
		t.Errorf("new page:\n%s", got)
	}
	if got := readFile(t, filepath.Join(w.Root, "unstamped.md")); strings.Contains(got, "generated") {
		t.Errorf("front-matter-only change must not stamp:\n%s", got)
	}
	if err := w.FinalizeProvenance(snap, now, " ", nil); err == nil {
		t.Error("empty producer must be refused")
	}
}

func TestMermaid(t *testing.T) {
	doc := "# D\n\n```mermaid\nflowchart TD\n  A --> end\n```\n\n````markdown\n```mermaid\nflowchart TD\n  A[\"x; y\"] --> B\n```\n````\n\n  ```mermaid\n  sequenceDiagram\n    A->>B: hi\n    loop every\n    end\n  ```\n\n```Mermaid\ngraph LR\n  A[a <b>] --> B\n```\n"
	fences := ExtractMermaid(doc)
	if len(fences) != 3 || fences[1].Indent != "  " {
		t.Fatalf("fences %+v", fences)
	}
	out, n := DegradeMermaid(doc)
	if n != 2 {
		t.Fatalf("degraded %d:\n%s", n, out)
	}
	if !strings.Contains(out, MermaidCommentPrefix+" and this diagram was converted") ||
		!strings.Contains(out, "```text\nflowchart TD\n  A --> end\n```") ||
		!strings.Contains(out, "```text\ngraph LR\n  A[a <b>] --> B\n```") ||
		!strings.Contains(out, "  ```mermaid\n  sequenceDiagram") ||
		!strings.Contains(out, "````markdown\n```mermaid\nflowchart TD\n  A[\"x; y\"]") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	if again, n := DegradeMermaid(out); n != 0 || again != out {
		t.Error("degrading is idempotent")
	}
	for _, valid := range []string{
		"flowchart LR\n  A[\"ok\"] --> B(\"fine\")\n",
		"flowchart LR\n  A[\"x; y <z>\"] --> B{\"n<br/>m\"}\n",
		"flowchart TD\n  rt -->|ingest| raw[raw/&lt;run-id&gt;/*.json]\n",
		"flowchart TD\n  A[line one<br>line two] --> B\n",
		"sequenceDiagram\n  Note over A: (phase -> generating); done\n",
	} {
		if r := MermaidHeuristic(valid); r != "" {
			t.Errorf("valid diagram flagged (%s):\n%s", r, valid)
		}
	}
	if s := sanitizeDiagnostic("a\n  b -- c --> d " + strings.Repeat("x", 400)); strings.Contains(s, "--") || strings.Contains(s, "\n") || len(s) > maxDiagnostic+4 {
		t.Errorf("sanitize: %q", s)
	}
}

func TestValidateLinks(t *testing.T) {
	w := newWiki(t, map[string]string{
		"a.md": "---\ntype: t\n---\n# Alpha Page\n\n## Section One\n\n## Section One\n\n" +
			"<!-- openwiki: broken internal link [old] stale. Fix the href or restore the target, then delete this comment. -->\n" +
			"[ok](b.md) [anchor](b.md#beta-heading) [self](#section-one-1) [ext](https://x.y) ![img](missing.png)\n" +
			"[bad](missing.md)\n" +
			"[badanchor](b.md#nope) [selfbad](#nope)\n" +
			"[src](../src/main.go#L10) [srcdir](../src/) [srcdir2](../src) [abs](/openwiki/b.md) [titled](b.md \"Title\")\n",
		"b.md": "---\ntype: t\n---\n# Beta Heading\n",
	})
	writeTree(t, w.Repo, map[string]string{"src/main.go": "package main\n"})
	r, err := w.ValidateLinks()
	if err != nil {
		t.Fatal(err)
	}
	var hrefs []string
	for _, is := range r.Issues {
		hrefs = append(hrefs, is.Href)
	}
	if strings.Join(hrefs, " ") != "missing.md b.md#nope #nope /openwiki/b.md" {
		t.Fatalf("issues %v", r.Issues)
	}
	content := readFile(t, filepath.Join(w.Root, "a.md"))
	if strings.Contains(content, "[old] stale") {
		t.Error("old stamp should be removed")
	}
	if !strings.Contains(content, "-->\n[bad](missing.md)") || strings.Count(content, "broken internal link") != 4 {
		t.Errorf("stamps:\n%s", content)
	}
	// Fixing the links removes the stamps on the next pass.
	writeTree(t, w.Root, map[string]string{"missing.md": "---\ntype: t\n---\n# Nope\n"})
	if _, err := w.ValidateLinks(); err != nil {
		t.Fatal(err)
	}
	content = readFile(t, filepath.Join(w.Root, "a.md"))
	if strings.Count(content, "broken internal link") != 3 || strings.Contains(content, "[missing.md]") {
		t.Errorf("after fix:\n%s", content)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Hello, World!":         "hello-world",
		"  Two  Spaces ":        "two--spaces",
		"Ünïcödé & Ñ":           "ünïcödé--ñ",
		"snake_case-and-dash":   "snake_case-and-dash",
		"`code` (in) headings?": "code-in-headings",
		"日本語の見出し":               "日本語の見出し",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFinalizeOrder(t *testing.T) {
	w := newWiki(t, map[string]string{"a.md": "---\ntype: t\n---\n# A\n"})
	snap, _ := w.SnapshotProvenance()
	var sawIndex bool
	_, err := w.Finalize(FinalizeOptions{
		Provenance: snap, Now: "2026-10-01T00:00:00.000Z", Producer: "owcli/1.0",
		ClaimSources: func() error {
			_, err := os.Stat(filepath.Join(w.Root, "index.md"))
			sawIndex = err == nil
			return nil
		},
	})
	if err != nil || !sawIndex {
		t.Fatalf("claim sources should run after index sync: %v %v", sawIndex, err)
	}
}
