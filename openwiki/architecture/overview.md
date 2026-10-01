---
type: Overview
title: Architecture Overview
description: How owcli is layered into Go packages, how a command flows from the CLI through generation, Claims, OKF finalization, and search, and what "compatible with upstream OpenWiki" means.
tags: [architecture, packages, data-flow, compatibility]
verified:
  - by: owcli/51f887a
    at: "2026-10-01T18:14:12.118Z"
sources:
  - id: openwiki-source-58776e6c955bcb51b8c7cf24
    resource: repo://cmd/owcli/main.go
  - id: openwiki-source-0542b60281e3aea77c59392e
    resource: repo://docs/design.md
  - id: openwiki-source-da21f52d07ab623ce6a4f0a7
    resource: repo://internal/cli/cli.go
  - id: openwiki-source-5be0d15fffd7a818d0c655c1
    resource: repo://internal/generate/generate.go
  - id: openwiki-source-558855d6164a3c50e05cc15a
    resource: repo://internal/generate/prompts.go
  - id: openwiki-source-38201bb963161492095c29eb
    resource: repo://internal/guide/guide.go
  - id: openwiki-source-5a4f8537ee47255d553406c9
    resource: repo://internal/okf/finalize.go
  - id: openwiki-source-6353eac56e48b42f7340a5d5
    resource: repo://internal/run/run.go
  - id: openwiki-source-737fd75f8183342d95459c99
    resource: repo://internal/search/search.go
  - id: openwiki-source-7cd39e52790a42c3eb3a3aa2
    resource: repo://internal/store/layout.go
generated: { by: "owcli/459c44e", at: "2026-10-01T18:01:43.250Z" }
---

# Architecture Overview

owcli is a clean-room Go reimplementation of the repository ("code") side of
[OpenWiki](https://github.com/langchain-ai/openwiki). It generates a linked
Markdown wiki for a Git repository with its own LLM agent loop, grounds the
wiki's factual statements in versioned source evidence ("Claims"), keeps the
output conformant with the Open Knowledge Format (OKF) v0.2, and searches it.
The behavioral specification lives in `docs/design.md`; that document also
lists what is deliberately out of scope (MCP server, parallel page workers,
the visualizer, coding-agent integrations, personal mode, workspaces,
GitHub Actions scheduling, telemetry, translation).

## Package layering

Everything lives under `internal/`, with `cmd/owcli/main.go` only calling
`cli.NewRootCommand().Execute()`. Dependencies point strictly downward:

```mermaid
flowchart TD
    cli["cli: cobra commands"] --> generate
    cli --> search
    cli --> run
    cli --> guide
    generate --> guide
    guide["guide: shared authoring standards"]
    generate["generate: planner and worker driver"] --> run
    generate --> agent
    run["run: resumable lifecycle"] --> claims
    run --> okf
    agent["agent: confined tools and loop"] --> llm
    llm["llm: providers"] --> config
    claims["claims: Claims model"] --> evidence
    claims --> okf
    search["search: FTS5 ranking"] --> claims
    evidence["evidence: repo:// resolver"] --> ignore
    okf["okf: front matter and finalization"] --> store
    claims --> store
    agent --> store
    agent --> ignore
```

Arrows point from a package to the packages it imports.

- **Leaf packages.** `store` (layouts, bindings, atomic JSON state), `ignore`
  (`.openwikiignore`), and `config` have no internal dependencies. See
  [Storage Layouts and Bindings](storage-and-bindings.md).
- **Deterministic core.** `evidence`, `claims`, `okf`, `search`, and `run`
  contain no model calls; every rule they enforce is testable without a model.
  See [Grounded Claims](../concepts/grounded-claims.md),
  [OKF Front Matter and Finalization](../concepts/okf-output.md), and
  [Wiki Search and Read](../concepts/search.md).
- **Model-facing layer.** `llm` speaks to providers, `agent` runs the tool
  loop over a confined workspace, and `generate` composes them with the `run`
  lifecycle. See [Model Providers](../integrations/model-providers.md) and
  [Generation Run Lifecycle](../workflows/generation-run.md).
- **Shared standards.** `guide` holds the planning, page, and Claim standards
  as text. owcli's own agent prompts and the instructions printed for
  interactive coding agents both use it, so there is one standard.

## How a command flows

`owcli init` / `owcli update` resolve configuration and the wiki layout, build
a provider, and call `generate.Generate`. That function calls `run.Begin`
(fresh, resumed, or a clean-update no-op), runs a planner agent if the run is
still planning, then loops over `run.Next`: snapshot the page, run a worker
agent, and either accept the page (the worker's `submit_page` tool calls
`run.SubmitPage`) or roll it back with `run.Skip`. Unrecoverable worker errors
abort the loop with the run left resumable; otherwise `run.Finish` performs
deterministic finalization.

During `Finish`, the OKF passes run in a fixed order and Claims evidence is
projected into front matter through the `ClaimSources` hook, which calls
`claims.SyncSources`, so `okf` never imports `claims`.

`owcli run begin|plan|next|inspect|submit|skip|finish` expose the same
lifecycle to an interactive coding agent, one JSON step per command, so the
agent researches and writes pages and owcli calls no model at all. See
[Agent-Driven Runs](../workflows/agent-driven-runs.md).

`owcli search`, `read`, `status`, and `check` never call a model. `search`
builds an in-memory SQLite FTS5 index per query and ranks wiki sections with
weighted BM25.

## Two storage layouts

Every component receives a resolved `store.Layout`. In the **in-repo** layout
the wiki is `<repo>/openwiki`; in the **external** layout it lives under
owcli's data directory and nothing is written into the repository, which is
how owcli explores other people's projects. In both cases page identifiers
look like `/openwiki/<path>.md`, and the repository's own top-level
`openwiki/` directory is never treated as source.

## Compatibility with upstream OpenWiki

The on-disk format matches upstream: directory layout, front matter
conventions, sidecar and manifest schemas, `repo://` evidence syntax, and
evidence version tokens. A wiki produced by either tool can be searched,
checked, and updated by the other. Opt-in compatibility tests prove this
against upstream's own wiki; see [Testing](../testing/overview.md).
