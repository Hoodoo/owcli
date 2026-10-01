---
type: Guide
title: Testing
description: How to build and test owcli, how model-dependent code is tested deterministically with a scripted provider, the fixture patterns used across packages, and the opt-in compatibility tests that compare owcli with upstream OpenWiki.
tags: [testing, compatibility, fixtures, ci]
verified:
  - by: owcli/70f8d76
    at: "2026-10-01T21:32:04.947Z"
sources:
  - id: openwiki-source-d46fb113abedebe5d8c15a4e
    resource: repo://internal/cli/e2e_test.go
  - id: openwiki-source-4b30a1d99e54458bf298f962
    resource: repo://internal/cli/generate.go
  - id: openwiki-source-385bf16ff5320b468557e102
    resource: repo://internal/evidence/compat_test.go
  - id: openwiki-source-7b3f1f61c82f6f8facf8911f
    resource: repo://internal/llm/llmtest/llmtest.go
  - id: openwiki-source-3f2ed63af48a9d13bb2096a6
    resource: repo://internal/okf/compat_test.go
  - id: openwiki-source-0eff31b693285d0d2305a525
    resource: repo://internal/search/compat_test.go
  - id: openwiki-source-012f2c78e3b1446dfc35803f
    resource: repo://Makefile
generated: { by: "owcli/70f8d76", at: "2026-10-01T21:32:50.382Z" }
---

# Testing

Every package has unit tests that run offline in a few seconds and need only
Go and `git` on `PATH`. Nothing in the default test run calls a model or the
network.

```sh
make check     # go vet ./... && go test ./...
make build     # bin/owcli, version stamped from git describe
make install   # build, then copy to $(BINDIR) (default ~/.local/bin) to try a change end to end
go test ./internal/run/ -run Resume -v
```

## Testing model-driven code without a model

`internal/llm/llmtest.Scripted` is a fake `llm.Provider`. It replays a fixed
list of turns and records every request, and helpers build the common turns:

- `Text` ends a turn with text;
- `Call` and `Calls` request tool calls;
- `LastResults` returns the tool results sent back on the latest request.

Because turns are plain functions, a test can make assertions about a request
and decide the reply in one place. For example, it can check that an evidence
error reached the model before submitting corrected Claims.

| Seam | Used by |
| --- | --- |
| `llmtest.Scripted` passed as `generate.Options.Provider` | `internal/generate` tests: a whole init with planner, worker, rejected submission, abandoned page, provider failure, and resume |
| `providerFactory` variable in `internal/cli` | CLI end-to-end tests run `owcli init/update/status/check/search` against a scripted model |
| `httptest.Server` | `internal/llm` tests check the exact HTTP requests (headers, body fields, verbatim thinking blocks) and retry behavior |
| scripted fake workers (no agent at all) | `internal/run` tests drive the lifecycle by writing pages and calling `SubmitPage` directly |

## Fixture patterns

- **Real Git repositories.** Tests that need a repository create one in
  `t.TempDir()` and commit through `git` with an inline identity
  (`-c user.name=t -c user.email=t@t`). Roots go through `EvalSymlinks`
  because temp directories may be symlinked.
- **Isolated owcli state.** CLI tests point `XDG_CONFIG_HOME` and
  `XDG_DATA_HOME` at temp directories and `chdir` into the repository, since
  commands act on the working directory.
- **External layout by default.** Lifecycle and CLI tests mostly use the
  external layout, then assert `git status --porcelain --ignored` is empty to
  prove nothing was written into the repository.
- **Injected clocks.** `run.Env.Now` makes timestamps deterministic.

## Upstream compatibility tests

Compatibility with upstream OpenWiki is tested against upstream's own wiki.
These tests skip unless their environment variables are set:

| Test | Package | Requires | Proves |
| --- | --- | --- | --- |
| `TestUpstreamWikiCompat` | okf | `OWCLI_UPSTREAM_DIR` | every upstream page validates and `Repair` leaves it byte-identical |
| `TestUpstreamFinalizeIsNoop` | okf | `OWCLI_UPSTREAM_DIR` | the full finalization pipeline changes no file in a copy of the wiki |
| `TestUpstreamSidecarCompat` | evidence | `OWCLI_UPSTREAM_DIR`, `OWCLI_UPSTREAM_SOURCE_DIR` | every evidence token is reproduced, freshly or via relocation |
| `TestUpstreamClaimsCompat` | claims | both of the above | sidecars load strictly; preflight finds no issues on pages verified at the source commit |
| `TestUpstreamSearchParity` | search | `OWCLI_UPSTREAM_DIR`, `OWCLI_UPSTREAM_PKG` (needs `node`) | refs and result content match upstream's `searchWiki` for a fixed query set |

`OWCLI_UPSTREAM_DIR` is a checkout of `langchain-ai/openwiki` (it contains
`openwiki/`). `OWCLI_UPSTREAM_SOURCE_DIR` is a checkout of the commit named
by a page's `gitHead` in the manifest; pages verified at other commits are
skipped. `OWCLI_UPSTREAM_PKG` is an installed `openwiki` npm package directory
(for example `/usr/lib/node_modules/openwiki`), whose
`dist/retrieval/wiki.js` the search test runs through `node`.

```sh
git clone https://github.com/langchain-ai/openwiki /tmp/ow
git -C /tmp/ow worktree add /tmp/ow-src <gitHead from .page-manifest.json>
OWCLI_UPSTREAM_DIR=/tmp/ow OWCLI_UPSTREAM_SOURCE_DIR=/tmp/ow-src \
OWCLI_UPSTREAM_PKG=/usr/lib/node_modules/openwiki go test ./internal/... -run Upstream -v
```

Related: [Wiki Search and Read](../concepts/search.md),
[Grounded Claims](../concepts/grounded-claims.md).
