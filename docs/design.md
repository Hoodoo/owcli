# owcli design

owcli is a clean-room Go reimplementation of the repository ("code") side of
[OpenWiki](https://github.com/langchain-ai/openwiki) (MIT). It generates and
maintains a linked Markdown wiki for a Git repository, grounds the wiki's
factual statements in versioned source evidence, and searches it.

This document is the behavioral specification the implementation works from.
It is written in our own words from upstream's documentation and observed
behavior. Upstream source may be consulted to resolve behavioral questions; it
is never copied.

Reference points:

- Upstream at commit `594ef3a2250dbe4ad5257b6b7f50923e2bc98227` (v0.6.1). Its
  own wiki under `openwiki/` is the most readable description of its behavior.
- [OKF v0.2 spec](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md).

## Scope

In scope:

- A CLI (`owcli`) that drives its own LLM agent loop to initialize and update a
  repository wiki.
- The storage backend: on-disk wiki layout, run checkpoint, page manifest,
  update metadata, Claims sidecars, and the binding registry.
- Grounded Claims: evidence resolution, staleness detection, sparse
  reconciliation, durability proofs, and projection into OKF front matter.
- `.openwikiignore` rules.
- OKF v0.2 output: front matter validation and repair, generation provenance,
  index synchronization, Mermaid validation/degradation, link validation.
- Search and section read over a wiki.
- Binding a repository **without writing artifacts into it** (external
  storage), for exploring other people's projects.

Out of scope: MCP server, parallel page workers, the visualizer UI, coding-agent
host integrations, personal mode and connectors, workspaces/linking, GitHub
Actions scheduling, telemetry, translation.

## Compatibility goal

The on-disk wiki format is compatible with upstream: same directory layout,
front matter conventions, sidecar and manifest schemas, and evidence URI
syntax. `owcli search` should work on a wiki upstream generated, and vice versa.
Evidence *version tokens* match upstream for both whole-file and line-range
evidence (verified against all 1,155 evidence entries of upstream's own wiki).
The one known divergence is files that are not valid UTF-8: upstream hashes
them after lossy decoding, owcli hashes raw bytes; the effect is a one-time
"stale" recheck, never data loss.

Compatibility tests are opt-in: set `OWCLI_UPSTREAM_DIR` to an upstream
checkout (and `OWCLI_UPSTREAM_SOURCE_DIR` to a checkout of a manifest
`gitHead` for evidence checks) and run `go test ./...`.

## Architecture

```
cmd/owcli            CLI entrypoint and commands
internal/ignore      .openwikiignore matcher
internal/store       layouts (in-repo / external), bindings, atomic JSON state
internal/okf         front matter, provenance, indexes, mermaid, links
internal/evidence    repo:// resources, resolver, versions, containment
internal/claims      sidecar store, session, mutations, preflight, reconcile, projection
internal/search      section units, tokenizer, BM25F ranking, section read
internal/llm         provider interface; Anthropic + OpenAI-compatible
internal/agent       confined file tools, tool-call loop, prompts
internal/run         generation lifecycle (begin/plan/next/submit/skip/finish)
```

All deterministic logic (everything except `llm` and the model-facing parts of
`agent`) must be testable without a model.

## Storage backend

### Layouts

A *wiki root* is the directory holding the wiki pages. Two layouts:

- **In-repo** (default, upstream-compatible): `<repo>/openwiki/`.
- **External** (`owcli bind --external`): `$XDG_DATA_HOME/owcli/wikis/<id>/openwiki/`
  (fallback `~/.local/share/...`). `<id>` is a stable slug + short hash of the
  repository's canonical absolute path. Nothing is written inside the repo: no
  wiki, no state files, no `AGENTS.md` block, no workflow.

`owcli bind --external --wiki-dir <dir>` (or `init --wiki-dir`) places an
external wiki in a chosen directory instead, e.g. inside a shared
knowledge-base repository; directories inside the documented repository are
refused, and `unbind --purge` never deletes a chosen directory.

**Versioning.** In-repo wikis are versioned by the repository. External
wikis are versioned by owcli: at bind time `Home` (the directory holding
`openwiki/`) becomes a Git repository unless it already lies inside one, with
`.run.json` and `.run-snapshots/` ignored; after every finished run owcli
commits only `Home`'s paths, with the mode, status, page counts, and the
documented source commit in the message. A shared repository's other staged
changes are left alone. History, review, revert, and sharing then use plain
Git in that directory.

**When to update.** The wiki documents the default branch. The agent
instructions trigger an update after merging into it (several merges may share
one run); `owcli run begin` reports the current and default branch and warns
when they differ.

The binding registry (`$XDG_CONFIG_HOME/owcli/bindings.json`) maps canonical
repo roots to their layout. Resolution: explicit registry entry, else in-repo if
`<repo>/openwiki/` exists, else unbound. All components receive a resolved
`Layout` and never compute paths themselves.

Evidence is always resolved against the repository, never the wiki root, and
wiki paths are always excluded from evidence and fingerprinting regardless of
layout.

### Files under the wiki root

| Path | Owner | Purpose |
| --- | --- | --- |
| `index.md` (every dir) | owcli | generated directory index; root carries `okf_version: "0.2"` |
| `quickstart.md` | model | mandatory entry page, generated last |
| `INSTRUCTIONS.md` | user | wiki-wide authoring guidance; preserved across init |
| `log.md` | reserved | never treated as a concept |
| `.claims/<page>.json` | owcli | Claims sidecar mirroring page path |
| `.run.json` | owcli | resumable run checkpoint; deleted on successful finish |
| `.page-manifest.json` | owcli | per-page completion record (`pageVersion`, `completedBy`, `completedRunId`, `gitHead`, `sourceFingerprint`) |
| `.last-update.json` | owcli | last run metadata (`updatedAt`, `command`, `gitHead`, `model`, `status`: complete/interrupted, `language`) |

All JSON state has a `schemaVersion`, is validated on read (malformed state is an
error, never silently discarded), and is written atomically (temp file with
exclusive create + rename).

## Ignore rules

`.openwikiignore` at the repo root, gitignore-like:

- blank lines and `#` comments skipped; backslashes normalized to `/`;
- `!` negates; last matching rule wins;
- leading `/` anchors to root; a pattern containing `/` is also anchored;
  otherwise it matches at any depth;
- trailing `/` = directory-only (also matches files below that directory);
  unlike upstream, a *file* whose name matches a directory-only pattern in a
  subdirectory (`cache/` vs file `a/cache`) is not ignored;
- `**/` = zero or more directories, `**` = anything, `*` = within a segment,
  `?` = one non-slash char; matching is case-insensitive;
- a match on a directory also covers everything below it.

`.git/` and the wiki root are always excluded. Rules are enforced by agent read
tools (hard error on read, silent drop from listings/globs/greps), evidence
resolution, and source fingerprinting.

## OKF v0.2 output

- Concept page = any `.md` under the wiki root except `index.md`, `log.md`,
  `INSTRUCTIONS.md` and hidden paths.
- Front matter validation reports issues instead of failing: required non-empty
  `type`; optional non-empty strings `title`, `description`, `resource`, legacy
  `timestamp`; `tags` list of non-empty strings; `generated` = `{by, at}`;
  `verified` = event or list of events; `sources` = list of mappings with
  non-empty `resource`; `status` in draft/stable/deprecated; `stale_after` and
  every `at` = real ISO 8601 datetime with explicit offset. Unknown keys are kept.
- Repair is conservative: valid front matter is untouched byte-for-byte; a
  parseable mapping gets surgical per-field fixes (missing `type` gets
  `Reference` + `openwiki_generated: true`; bad `title` re-derived from first H1
  or filename; bad optional scalars and unprovable trust fields removed;
  `sources`/`verified` filtered to valid entries); only unparseable YAML is
  replaced with a minimal `type`/`title`/`openwiki_generated` block.
- Front matter edits are line-preserving (set/replace/remove single fields,
  replace one structured field) so producer extensions survive.
- Authors own `type`, `title`, `description`, `tags`; owcli owns `generated`,
  `verified`, `sources`.
- Generation provenance: before authoring, snapshot SHA-256 of every page body
  (front matter excluded) plus its prior `generated`; after, stamp
  `{by: owcli/<version>, at: now}` on new/changed bodies (dropping `timestamp`),
  restore the prior event on unchanged bodies.
- Index sync: an `index.md` per directory listing files (title + description,
  falling back to basename) and subdirectories, sorted by href, written only
  when content changes. Also reports pages with code-derived metadata or no
  description (informational).
- Mermaid: extract ```` ```mermaid ```` fences (ignoring fences nested in longer
  fences); a conservative heuristic flags near-certain flowchart breakage (`end`
  as a node id, `;` or `<`/`>` inside an *unquoted* label, after ignoring HTML
  entities and `<br>`); invalid fences become ```` ```text ```` preceded by
  `<!-- openwiki: mermaid parse failed ... -->` so a later update can repair
  them. owcli has no authoritative Mermaid parser (upstream optionally loads
  mermaid.js), so the heuristic is stricter than upstream's about false
  positives: it never flags a diagram upstream's real parser accepted.
- Internal link validation: relative links and GitHub-style heading anchors;
  broken links get a `<!-- openwiki: broken internal link ... -->` stamp above
  the line (old stamps are removed first); root-absolute links are flagged.
  Targets resolve in repository coordinates with the wiki at `/openwiki`, so
  links from pages to source files work in the external layout too. Unlike
  upstream, a link to a directory without a trailing slash is not flagged.
- Finalization order: Mermaid → indexes → links → claim sources → provenance.

## Grounded Claims

- **Claim** = `{id, statement, evidence[]}`; **Evidence** = `{resource, version}`.
  Ids are `claim_<32 hex>`, globally unique across the wiki.
- **Resource** = `repo://<repo-relative path>[#Lx-Ly]`; `#L8` canonicalizes to
  `#L8-L8`. Rejected: escaping paths, absolute/drive paths, control chars,
  `.git`, the wiki root, ignored paths.
- **Resolver** reads through a containment gate (no symlinks/aliases escaping
  the physical repo root). Versions: `repo-file-v1:sha256:<hex>` for whole files;
  `repo-lines-v1:sha256:<hex>:<base64 anchors>` for ranges, where anchors
  (selected line count, first/last selected line hashes, up to 3 lines of
  preceding/following context with hashes) let the resolver relocate moved but
  unchanged text. Missing file/range resolves to *null* (unresolved), distinct
  from a changed version (stale). Containment violations count as unresolved.
  Resolution is cached per `(resource, previousVersion)` within one phase only.
- **Sidecar** `.claims/<page path>.json`: `schemaVersion`, `pageVersion`
  (`sha256:` of the page's exact bytes), `claims`, optional `verification`
  event. Only concept pages get sidecars.
- **Preflight** re-resolves all evidence: unresolved beats stale; issues are
  sorted deterministically and attached to owning pages. Issues demand a
  recheck, not a retraction. Unlike upstream, evidence that has become
  *invalid* (e.g. newly matched by `.openwikiignore`) is reported as
  unresolved instead of aborting the run.
- **Mutations** `add/confirm/update/retract` apply as an all-or-nothing batch;
  no duplicate targets, no unknown ids, no duplicate evidence; confirm and
  evidence-less update refresh versions. Success marks the page dirty and clears
  issues for the targeted ids.
- **Sparse reconciliation** per page submission: `confirmedClaimIds`, `claims`
  (with id = revise, without id = new, matched to an identical existing claim
  first), `retractedClaimIds`. Omitted issue-free claims are confirmed
  automatically; omitted issue-bearing claims reject the submission; retracting
  an already-absent id is tolerated; a factual page may not end with zero claims.
- **Durability** on finalize: refuse pages with evidence debt; re-resolve all
  evidence one last time; hash the page; write the sidecar; add a
  `verification` event when claims are non-empty. Per-page proof re-reads the
  sidecar and checks persisted state, `pageVersion`, verification, its
  projection into `verified`, and claim/evidence equality. Finish also removes
  orphan sidecars.
- **Projection**: evidence → OKF `sources` (collapsed to whole-file resources,
  deterministic ids `openwiki-source-<hash>`, foreign entries kept); durable
  verification → one `verified` event appended after events by other actors
  (both `owcli/*` and upstream `openwiki/*` events count as machine-owned and
  are replaced, so either tool can take over a wiki); page
  versions refreshed after projection, rolling back the stamp if refresh fails.

## Generation lifecycle

Six deterministic operations with a durable checkpoint, driven sequentially by
the native agent runner:

1. **begin** — validate language; if a checkpoint exists, resume (reset
   skipped jobs to pending; drop the plan if the source fingerprint changed);
   otherwise start fresh. A fresh update runs preflight and returns *noop* when
   the tree is clean, there are no grounding issues, and every page has manifest
   coverage. A fresh init backs up the existing wiki (keeping `INSTRUCTIONS.md`)
   and restores it if begin fails before the checkpoint is durable.
2. **submit plan** — normalize/dedupe paths, reject reserved pages, no page both
   generated and deleted; init must include `quickstart.md` and may not delete;
   updates get extra jobs for pages with claim issues; sort by path with
   `quickstart.md` last; each job gets a UUID and `pending`.
3. **next page** — first pending job with `existing`, `existingClaimCount`, and
   only the issue-bearing claims. Read-only.
4. **inspect page claims** — full compact claim set of the current job only.
5. **submit page** — current job only; repair front matter; reconcile claims;
   finalize that page (excluding other pending pages); prove durability; update
   manifest; then mark complete in the checkpoint.
6. **finish** — no pending jobs allowed; check fingerprint before and after;
   delete abandoned/planned-deleted pages and their sidecars; run OKF
   finalization; restore skipped pages from snapshots; finalize claims excluding
   skipped pages; prove whole-wiki durability; rebuild manifest (restamp only
   pages this run completed); write `.last-update.json` (`interrupted` if any
   skip or drift); delete the checkpoint last.

Worker failure before submit → restore the page and sidecar from the pre-worker
snapshot and mark the job `skipped`. Failure after a successful submit never
rolls back. The **source fingerprint** hashes HEAD plus the path and content
of every tracked and untracked, non-git-ignored, non-`.openwikiignore`d file,
excluding the wiki.

owcli specifics (implemented in `internal/run`):

- `.run.json` carries `producer: "owcli"`; a checkpoint written by another
  tool is reported, never resumed or discarded.
- Clean-update no-op: no user message, last run `complete` at the current
  HEAD, clean source tree, no grounding issues, and every page's manifest
  `pageVersion` matching its bytes.
- Init backs the old wiki up to a temporary directory, clears it (keeping
  `INSTRUCTIONS.md`, seeding a default one if absent), and restores the backup
  if begin fails before the checkpoint is durable.
- Finish restores skipped pages *before* the OKF passes (so indexes describe
  the restored pages), deletes pages left behind by a superseded plan, and
  marks the run `interrupted` when pages were skipped or source drifted.
- While a page is submitted, other pending pages are excluded from Claims
  finalization *and* from verification projection.
- Only English is written; translation is out of scope.

## Agent runtime

- `llm.Provider` interface: one chat call with system prompt, messages, and
  tool definitions, returning text and tool calls. Implementations: Anthropic
  Messages API and OpenAI-compatible Chat Completions (base URL configurable),
  raw HTTP with retries on 408/409/429/5xx and connection errors (honoring
  `retry-after`).
- Anthropic specifics: default model `claude-opus-5-5`, `effort` set
  explicitly (default `high`; the model's own default is `medium`); thinking
  left at the model default and never disabled; assistant content (including
  thinking blocks) resent verbatim so history is append-only; `tool_choice`
  only `auto` (forced tool use is rejected by current models); top-level
  `cache_control` for the stable tools+system prefix; server-side refusal
  fallbacks (`fallbacks: "default"`, beta `server-side-fallback-2026-07-01`)
  on by default, disable with `no_fallbacks = true` for proxies/other platforms.
- Requests are non-streaming with `max_tokens` 16000; streaming is a possible
  later improvement for very long page writes.
- Workspace: one virtual tree where `openwiki/...` maps to the wiki root (in
  either layout) and everything else to the repository. The repository's own
  `openwiki/` directory is hidden (in the external layout it may be a stale
  upstream wiki). Reads honor `.openwikiignore`, refuse `..` segments and
  symlink escapes, and never show the wiki's hidden control files.
- Tools: `ls`, `glob`, `grep`, `read_file` (line-numbered, paged), `git_log`
  (fixed arguments) over the workspace; `write_file`/`edit_file` confined to
  the current job's page; no shell. Lifecycle actions (`submit_plan`,
  `submit_page`, `inspect_page_claims`) are exposed to the model as tools; a
  tool can end the loop (e.g. a successful `submit_page`).
- Loop: tool failures go back to the model as error results; a `max_tokens`
  stop without tool calls gets a continue nudge; refusals and the step limit
  (default 60 model calls) end the run with an error.
- Planner agent produces the plan; one worker agent per page job, run
  sequentially. Prompts and claim guidance are written fresh for owcli.

## Agent-driven runs

Interactive coding agents can drive the lifecycle themselves; owcli then calls
no model. `owcli run begin|plan|next|inspect|submit|skip|finish` are stateless
per invocation (each reattaches to `.run.json` via `run.Open`), take JSON on
stdin or `--file`, and print one JSON object with a `next` hint; errors print
`{"error":{"code","message"}}` with the lifecycle error code and exit 1.
`next` persists the pre-edit snapshot under `<wiki>/.run-snapshots/` so `skip`
and `finish` work across processes; `finish` removes it. `begin update`
reports `changedPaths` since the last documented commit (committed,
uncommitted, and untracked; ignored and wiki paths left out) and per-page
Claim issue counts. Update plans may list no pages: pages with Claim issues
are added automatically. Agent-driven runs record `host-agent` as the model.

Instructions follow Kata's pattern: `owcli agents-md` writes a compact
routing block (rules plus a dot digraph) into `AGENTS.md` between managed
markers (refused for external bindings; `--print` for global agent config),
and `owcli quickstart` prints the full procedure, JSON formats, and the
planning, page, and Claim standards. Those standards live in
`internal/guide` and are shared with owcli's own planner and worker prompts.

## Search

Upstream's "semantic" search is lexical. owcli reproduces it:

- Units: for each non-deprecated concept page, one unit per H2 section (except
  "Related pages/reading/links", "See also", "Navigation"), plus an
  introduction unit (H1 text before the first H2) carrying only heading and
  prose; a page with no H2 is one unit. Anchors are GitHub-style heading slugs
  with `-N` dedup suffixes.
- Fields and weights: title 8, description 4, heading 6, prose 1, identifiers
  3 (page path, tags, source paths). Text is expanded with camelCase/acronym and
  `_ . / : # -` splits; Porter stemming over Unicode word tokens.
- Query: unique lowercase word tokens (max 64), stop words dropped unless
  nothing remains; OR semantics.
- Ranking: source-path-hint matches desc → number of query terms matched desc
  → BM25 → stable order. Results: `page#anchor` ref, title, section, description,
  best-matching block excerpt (≤600 chars). Default 5, max 20.
- Read: return full raw section(s) for `page` + anchors.
- An interface is left for an optional embedding reranker later.
- Implementation: FTS5 via `modernc.org/sqlite` (pure Go, no cgo) for exact
  tokenizer/BM25 parity, and goldmark for heading structure. `modernc.org/sqlite`
  is pinned to v1.34.5 because newer releases need Go ≥ 1.26 and the toolchain
  here is 1.22. Verified identical to upstream (refs and result content) on 18
  queries over upstream's wiki (`OWCLI_UPSTREAM_PKG` compat test).

## CLI

```
owcli bind [--external] [path]     register a repo; in-repo or external layout
owcli unbind [--purge] [path]      forget a binding (optionally delete external wiki)
owcli init [--external] [message]  generate a wiki from scratch; binds an unbound repo
owcli update [message]             incremental update driven by drift and claim issues
owcli status                       binding, last update, pending run, claim health
owcli check                        read-only: preflight, OKF, links, diagrams; non-zero exit on problems
owcli search <query> [--path p]    ranked section search
owcli read <ref | page anchor...>  print sections
owcli run <step>                   agent-driven lifecycle (see Agent-driven runs)
owcli quickstart                   full guide for coding agents
owcli agents-md [--print]          compact routing block for AGENTS.md
```

`init`/`update` resume an interrupted run of the same kind (Ctrl-C keeps
completed pages). `--agents-md` adds or refreshes a managed pointer block
(upstream's `OPENWIKI:START/END` markers) in `AGENTS.md` and an existing
`CLAUDE.md`; it is refused for external wikis. `-v` prints every tool call.

Configuration: provider, model, base URL, API key env var name, effort,
fallback opt-out — via flags, env (`OWCLI_*`), or
`$XDG_CONFIG_HOME/owcli/config.toml`. The OpenAI-compatible provider has no
default model; local endpoints (localhost) need no API key.
