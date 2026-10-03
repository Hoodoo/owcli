package search

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hoodoo/owcli/internal/claims"
	"github.com/Hoodoo/owcli/internal/store"
)

func wiki(t *testing.T, pages map[string]string) *claims.Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "openwiki")
	for rel, content := range pages {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return claims.NewStore(store.Layout{WikiRoot: root})
}

const retryPage = `---
type: concept
title: Retry Handling
description: How failed provider calls are retried.
tags: [resilience]
sources:
  - id: s1
    resource: repo://src/net/retry.go
---

# Retry Handling

Calls to providers can fail transiently.

### Background

Older versions never retried.

## Backoff policy

The retryHandler waits with exponential backoff between attempts.

` + "```go\n# not a heading\nfunc retry() {}\n```" + `

## Limits

At most five attempts are made.

## Related pages

- [Errors](errors.md) about retry
`

const errorsPage = `---
type: concept
title: Error Model
description: Error classes.
---

# Error Model

Errors are classified. A retry is mentioned once.
`

func refs(rs []Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Ref[0])
	}
	return out
}

func TestUnits(t *testing.T) {
	us := units(retryPage, "/openwiki/concepts/retry.md", []string{"backoff"})
	var got []string
	for _, u := range us {
		got = append(got, u.ref+"|"+u.heading)
	}
	want := []string{
		"openwiki/concepts/retry.md#retry-handling|",
		"openwiki/concepts/retry.md#backoff-policy|Backoff policy",
		"openwiki/concepts/retry.md#limits|Limits",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("units %v", got)
	}
	intro := us[0]
	if !intro.introductionOnly || !strings.Contains(intro.prose, "fail transiently") || !strings.Contains(intro.prose, "never retried") || strings.Contains(intro.prose, "### Background") {
		t.Errorf("intro prose %q", intro.prose)
	}
	if !strings.Contains(us[1].prose, "# not a heading") {
		t.Error("code fence content belongs to its section")
	}
	if us[1].identifiers != "openwiki/concepts/retry.md resilience src/net/retry.go" {
		t.Errorf("identifiers %q", us[1].identifiers)
	}
	if us[1].excerpt != "The retryHandler waits with exponential backoff between attempts." {
		t.Errorf("excerpt %q", us[1].excerpt)
	}

	if us := units("---\ntype: t\nstatus: deprecated\n---\n# X\n\n## Y\n", "/openwiki/x.md", nil); us != nil {
		t.Error("deprecated pages are not searchable")
	}
	single := units("---\ntype: t\n---\n# Only\n\nText.\n\n## Related pages\n\n- x\n", "/openwiki/only.md", nil)
	if len(single) != 1 || single[0].ref != "openwiki/only.md#only" || single[0].heading != "" {
		t.Errorf("single unit %+v", single)
	}
	if us := units("---\ntype: t\n---\nNo headings at all.\n", "/openwiki/n.md", nil); us != nil {
		t.Error("headingless pages have no units")
	}
}

func TestHeadingsAndAnchors(t *testing.T) {
	src := []byte("# Title *em* `code`\n\nSetext Heading\n--------------\n\n> # quoted\n\n## Dup\n\n## Dup\n\n## Ünïcode & [link](x.md)\n")
	hs := headings(src)
	var got []string
	for _, h := range hs {
		got = append(got, h.anchor)
	}
	if strings.Join(got, ",") != "title-em-code,setext-heading,dup,dup-1,ünïcode--link" {
		t.Fatalf("anchors %v", got)
	}
	secs := sections(src, hs)
	if secs[1].raw != "Setext Heading\n--------------\n\n> # quoted" {
		t.Errorf("setext section %q", secs[1].raw)
	}
	if !strings.HasPrefix(secs[0].raw, "# Title") || !strings.HasSuffix(secs[0].raw, "[link](x.md)") {
		t.Errorf("h1 section spans the document: %q", secs[0].raw)
	}
}

func TestQueryTerms(t *testing.T) {
	if got := strings.Join(queryTerms("How does the retryHandler work?"), ","); got != "retryhandler,work,retry,handler" {
		t.Errorf("terms %s", got)
	}
	if got := strings.Join(queryTerms("how is it"), ","); got != "how,is,it" {
		t.Errorf("stop-word-only query keeps words: %s", got)
	}
	if got := queryTerms("___ !!!"); len(got) != 0 {
		t.Errorf("no word terms: %v", got)
	}
}

func TestSearchRanking(t *testing.T) {
	st := wiki(t, map[string]string{"concepts/retry.md": retryPage, "concepts/errors.md": errorsPage, "index.md": "# Files\n\nretry retry retry\n", ".hidden/x.md": "# Retry\n\nretry\n"})
	rs, err := Search(st, Request{Query: "retry backoff"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := refs(rs)
	if len(got) == 0 || got[0] != "openwiki/concepts/retry.md#backoff-policy" {
		t.Fatalf("results %v", got)
	}
	for _, r := range got {
		if strings.Contains(r, "index.md") || strings.Contains(r, ".hidden") {
			t.Errorf("structural or hidden page returned: %s", r)
		}
	}
	if !strings.HasPrefix(rs[0].Content, "Retry Handling\nSection: Backoff policy\nHow failed provider calls are retried.\n") {
		t.Errorf("content %q", rs[0].Content)
	}
	if rs[0].Kind != "section" {
		t.Error("kind")
	}

	// A source path hint lifts sections citing that path to the top.
	rs, err = Search(st, Request{Query: "errors classified retry", Paths: []string{"net/retry.go"}, Limit: 1}, Options{})
	if err != nil || len(rs) != 1 || !strings.HasPrefix(rs[0].Ref[0], "openwiki/concepts/retry.md") {
		t.Fatalf("path hint: %v %v", refs(rs), err)
	}

	// Stemming: "attempt" matches "attempts".
	rs, _ = Search(st, Request{Query: "attempt limit"}, Options{})
	if len(rs) == 0 || rs[0].Ref[0] != "openwiki/concepts/retry.md#limits" {
		t.Errorf("stemming: %v", refs(rs))
	}
}

func TestSearchValidation(t *testing.T) {
	st := wiki(t, map[string]string{"a.md": errorsPage})
	bad := []Request{
		{Query: "  "},
		{Query: strings.Repeat("x", MaxQueryChars+1)},
		{Query: "x", Limit: 21},
		{Query: "x", Limit: -1},
		{Query: "x", Paths: []string{"../etc"}},
		{Query: "x", Paths: []string{"/abs"}},
		{Query: "x", Paths: []string{"src/*.go"}},
		{Query: "x", Paths: make([]string, 21)},
	}
	for i, r := range bad {
		if _, err := Search(st, r, Options{}); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("case %d: want ErrInvalidRequest, got %v", i, err)
		}
	}
	if rs, err := Search(st, Request{Query: "!!!"}, Options{}); err != nil || len(rs) != 0 {
		t.Errorf("termless query: %v %v", rs, err)
	}
	empty := wiki(t, nil)
	if rs, err := Search(empty, Request{Query: "x"}, Options{}); err != nil || len(rs) != 0 {
		t.Errorf("empty wiki: %v %v", rs, err)
	}
}

type reverse struct{}

func (reverse) Rerank(_ string, c []Result) ([]Result, error) {
	out := make([]Result, len(c))
	for i := range c {
		out[i] = c[len(c)-1-i]
	}
	return out, nil
}

func TestReranker(t *testing.T) {
	st := wiki(t, map[string]string{"concepts/retry.md": retryPage, "concepts/errors.md": errorsPage})
	plain, _ := Search(st, Request{Query: "retry", Limit: 20}, Options{})
	re, err := Search(st, Request{Query: "retry", Limit: 1}, Options{Reranker: reverse{}})
	if err != nil || len(re) != 1 || re[0].Ref[0] != plain[len(plain)-1].Ref[0] {
		t.Fatalf("reranker should see all candidates: %v vs %v (%v)", refs(re), refs(plain), err)
	}
}

func TestRead(t *testing.T) {
	st := wiki(t, map[string]string{"concepts/retry.md": retryPage})
	page, secs, err := Read(st, "concepts/retry.md", []string{"#limits", "retry-handling"})
	if err != nil {
		t.Fatal(err)
	}
	if page != "openwiki/concepts/retry.md" || len(secs) != 2 || secs[0].Content != "## Limits\n\nAt most five attempts are made." {
		t.Fatalf("read %s %+v", page, secs)
	}
	if !strings.Contains(secs[1].Content, "## Related pages") {
		t.Error("H1 section includes the whole page")
	}
	for _, c := range []struct {
		page    string
		anchors []string
	}{
		{"concepts/retry.md", nil},
		{"concepts/retry.md", []string{"nope"}},
		{"concepts/retry.md", []string{"a/b"}},
		{"index.md", []string{"x"}},
		{"../x.md", []string{"x"}},
		{".hidden/x.md", []string{"x"}},
	} {
		if _, _, err := Read(st, c.page, c.anchors); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("Read(%s, %v): want ErrInvalidRequest, got %v", c.page, c.anchors, err)
		}
	}
}

func TestSearchWikisRanksAcrossWikis(t *testing.T) {
	a := wiki(t, map[string]string{"concepts/errors.md": errorsPage})
	b := wiki(t, map[string]string{"concepts/retry.md": retryPage})
	both := wiki(t, map[string]string{"concepts/errors.md": errorsPage, "concepts/retry.md": retryPage})
	req := Request{Query: "retry backoff", Limit: MaxResults}

	rs, err := SearchWikis([]Source{{Store: a, Wiki: "a"}, {Store: b, Wiki: "b"}}, req, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// One index over both wikis ranks exactly like one wiki holding both pages.
	single, err := Search(both, req, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(refs(rs), " ") != strings.Join(refs(single), " ") {
		t.Fatalf("federated %v != combined %v", refs(rs), refs(single))
	}
	for _, r := range rs {
		want := "b"
		if strings.Contains(r.Ref[0], "errors.md") {
			want = "a"
		}
		if r.Wiki != want {
			t.Errorf("%s: wiki = %q, want %q", r.Ref[0], r.Wiki, want)
		}
	}
	if rs[0].Wiki != "b" || rs[len(rs)-1].Wiki != "a" {
		t.Errorf("ranking did not interleave by relevance: %+v", rs)
	}
	for _, r := range single {
		if r.Wiki != "" {
			t.Errorf("standalone result carries a wiki: %+v", r)
		}
	}
}
