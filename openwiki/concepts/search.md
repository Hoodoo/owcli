---
type: Concept
title: Wiki Search and Read
description: How owcli search ranks wiki sections lexically with an in-memory SQLite FTS5 index and weighted BM25, re-orders by source-path hints and query-term coverage, builds excerpts, leaves a hook for semantic reranking, and how owcli read returns whole sections.
tags: [search, retrieval, fts5, bm25, ranking]
verified:
  - by: openwiki/0.6.1
    at: 2026-10-01T17:37:16.322Z
sources:
  - id: openwiki-source-a5b2fc2f70918963e2e2700c
    resource: repo://internal/search/markdown.go
  - id: openwiki-source-737fd75f8183342d95459c99
    resource: repo://internal/search/search.go
generated: { by: "claude-code", at: "2026-10-01T17:37:16.322Z" }
---

# Wiki Search and Read

Upstream OpenWiki's "semantic" search is actually lexical, and owcli
reproduces it exactly: for the same wiki and query, the ranked refs and the
result text are byte-identical to upstream's (see
[Testing](../testing/overview.md)). Everything lives in `internal/search`
and is model-free. `owcli search` and `owcli read` are thin CLI wrappers.

## Search units

The wiki is not indexed as whole pages. Each retrievable page is split into
*units*:

- one unit per H2 section, except navigation sections ("Related pages",
  "Related reading", "Related links", "See also", "Navigation");
- an **introduction** unit for the text between the H1 and the first H2. It
  carries only heading and prose, so a page's title and description do not
  count twice;
- a page without qualifying H2 sections becomes one unit spanning its whole
  body (if it has an H1); a page with no headings has no units.

Pages with `status: deprecated`, empty bodies, or a hidden path segment are
skipped, as are structural pages (`index.md`, `log.md`, `INSTRUCTIONS.md`).

Headings come from goldmark's block parser, so a `#` inside a fenced code
block is not a heading and setext headings count. Anchors are GitHub-style
slugs: lowercase, drop everything but letters, numbers, `_`, `-`, and
whitespace, turn spaces into `-`, and suffix duplicates with `-1`, `-2`, and
so on. A result ref looks like `openwiki/concepts/x.md#anchor`.

## Indexing and ranking

Every query builds a throwaway in-memory SQLite database (pure-Go
`modernc.org/sqlite`, one connection) with an FTS5 table tokenized as
`porter unicode61`. Each unit fills five columns, with these BM25 weights:

| Column | Content | Weight |
| --- | --- | --- |
| title | page title | 8 |
| description | page description | 4 |
| heading | section heading | 6 |
| prose | section Markdown | 1 |
| identifiers | page path, tags, `sources` paths | 3 |

All text is expanded with a split copy, so identifiers match their parts:
`retryHandler` also yields `retry Handler`, and `src/x.go` yields `src x go`.
Query terms are the distinct lowercase word tokens of the expanded query, at
most 64. Stop words are dropped unless nothing else remains. The terms are
ORed together.

Matching units are ordered by four keys:

1. how many `--path` hints match one of the unit's source paths (equal, or one
   path a suffix of the other);
2. how many query terms the unit contains;
3. BM25 score;
4. insertion order.

Hints therefore lift relevant sections, but never pull in sections that
match no query term.

## Results

A result is `{kind: "section", ref: [...], content}`. The content joins:

- the page title;
- `Section: <heading>`;
- the description;
- an **excerpt**: the block of the section containing the most query terms
  (earliest on ties), with links and images reduced to their text, Markdown
  punctuation removed, whitespace collapsed, and cut at a word boundary
  around 600 characters.

Limits: queries up to 2,000 characters, 1-20 results (default 5), and up to
20 path hints, which must be repository-relative without traversal or globs.
Invalid requests return `ErrInvalidRequest`.

## Reranker hook

`Options.Reranker` is the extension seam for a later semantic stage, for
example embeddings. When set, the lexical stage passes the top 20 candidates
to `Rerank` and truncates its answer to the requested limit. No reranker
ships today.

## Read

`Read(page, anchors)` returns complete sections: the heading through the end
of its subtree, by anchor, in request order. Pages may be written as
`concepts/x.md`, `openwiki/concepts/x.md`, or `/openwiki/concepts/x.md`.
`owcli read` also accepts a search ref directly
(`owcli read openwiki/concepts/x.md#anchor`). Unknown anchors, structural
pages, and traversal are rejected.
