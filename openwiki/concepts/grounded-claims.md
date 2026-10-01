---
type: Concept
title: Grounded Claims and Evidence
description: How owcli ties wiki statements to versioned repository evidence - repo:// resources, content-derived version tokens with line relocation, the Claims store/session/runtime split, preflight staleness detection, sparse reconciliation, durability proofs, and projection into OKF front matter.
tags: [claims, evidence, grounding, provenance, verification]
verified:
  - by: openwiki/0.6.1
    at: 2026-10-01T17:37:16.322Z
sources:
  - id: openwiki-source-46f044de4ec4da88d15d5cfe
    resource: repo://internal/claims/mutations.go
  - id: openwiki-source-ee555aa6533616fea36c8eec
    resource: repo://internal/claims/preflight.go
  - id: openwiki-source-d15ce68143d484e078a5e9b4
    resource: repo://internal/claims/project.go
  - id: openwiki-source-3fa6899be5ffb38c9497e106
    resource: repo://internal/claims/reconcile.go
  - id: openwiki-source-968199c466a6e9f2f9a8c1ee
    resource: repo://internal/claims/runtime.go
  - id: openwiki-source-da48efe6aa37e3f60cde9d7f
    resource: repo://internal/claims/session.go
  - id: openwiki-source-f4e0277be3f658e33820b765
    resource: repo://internal/claims/store.go
  - id: openwiki-source-84cd3b3bc4cd5a6b41d105fa
    resource: repo://internal/evidence/resolver.go
  - id: openwiki-source-338973cba1508ded8362d7d4
    resource: repo://internal/evidence/resource.go
generated: { by: "claude-code", at: "2026-10-01T17:37:16.322Z" }
---

# Grounded Claims and Evidence

A **Claim** is one atomic, independently checkable proposition about the
repository, backed by one or more **evidence** citations. Every grounded wiki
page owns a complete Claim set. owcli records the version of each cited
resource when the Claim is accepted and, on every later run, rechecks whether
that source still says the same thing. Two packages implement this:
`internal/evidence` (addressing and versioning source) and `internal/claims`
(the Claim model and its persistence). Both are deterministic; no model call
is involved.

## Evidence resources

Evidence is addressed as `repo://<repository-relative path>[#Lstart-Lend]`.
`evidence.Parse` normalizes and validates it. `#L8` becomes `#L8-L8`,
backslashes and `./` segments are normalized, and percent-encoding is
decoded. It rejects:

- paths that escape the repository, and absolute or drive-letter paths;
- control characters and invalid percent-encoding;
- any path under `.git` or `openwiki` (compared case-insensitively), so a
  wiki can never cite itself.

The canonical form percent-encodes each path segment the way JavaScript's
`encodeURIComponent` does. That makes resources byte-identical to upstream
OpenWiki's.

## Resolution and version tokens

`RepoResolver.Resolve(resource, previousVersion)` reads the file through a
containment gate:

- a symbolic link, or a path whose physical location differs from its
  logical one, is an `ErrSecurity`;
- a path excluded by `.openwikiignore` is invalid evidence;
- a missing file, directory, or out-of-range line span resolves to `nil`.
  "Gone" is deliberately distinct from "changed".

Version tokens are content-derived, so a version mismatch means the cited
content changed:

- **Whole file:** `repo-file-v1:sha256:<hash of the file>`.
- **Line range:** `repo-lines-v1:sha256:<hash of the selected lines>:<anchors>`.
  The anchors are base64url JSON with the selected line count, hashes of the
  first and last selected lines, and hashes of up to three lines of
  surrounding context on each side.

When a previous version is supplied, the resolver relocates moved text:

1. If the hinted span still hashes to the previous content, nothing changed.
2. Otherwise, a unique span with the same first line, last line, and content
   is found anywhere in the file. When several match, the context hashes break
   the tie. Moved text keeps its old token and gets a corrected `#L` range.
3. If the content changed, the region between the surviving preceding and
   following contexts becomes the new span with a fresh token.
4. If no unique region exists, the evidence is unresolved.

`evidence.Cached` memoizes `(resource, previousVersion)` pairs. Callers wrap
the resolver freshly for each phase (preflight, one mutation batch, one
finalization), so a cached answer never crosses a freshness boundary.

## Store, session, runtime

| Layer | Responsibility |
| --- | --- |
| `Store` | sidecars under `<wiki>/.claims/<page>.json`; page discovery, Markdown read/write, page hashing; refuses symlinked or aliased paths |
| `Session` | run-scoped Claim state per page, globally unique Claim ids, grounding issues, atomic mutations, finalization |
| `Runtime` | wires store, resolver, and session; `Finalize` plus the durability proofs |

A sidecar holds `schemaVersion` 1, `pageVersion` (`sha256:` of the page's
exact bytes), the `claims` array, and an optional `verification` `{by, at}`.
Sidecars are validated strictly: unknown fields, whitespace-padded values,
duplicate ids, and duplicate evidence are all errors. Only concept pages own
sidecars; `index.md`, `log.md`, and `INSTRUCTIONS.md` never do.

## Preflight: stale and unresolved

`RunPreflight` re-resolves every persisted evidence entry against its
recorded version:

- **unresolved:** some resource no longer resolves. That includes security
  refusals and, unlike upstream, evidence that has since become invalid, for
  example newly ignored.
- **stale:** everything resolves but at least one version changed.

Unresolved wins over stale. Issues are sorted deterministically and attached
to their pages; sidecars whose page vanished are reported as orphans. An issue
means "recheck this", not "retract this".

## Mutations and sparse reconciliation

`Apply` runs a batch of `add`, `confirm`, `update`, and `retract` operations
as one transaction. All evidence is resolved before anything changes, so one
bad citation rejects the whole batch. Confirming re-resolves the existing
evidence, refreshing its versions.

Page workers never resubmit their whole Claim set. `Reconcile` turns a sparse
`Proposal` into a complete batch:

- `confirmedClaimIds` are rechecked and kept;
- a proposed Claim with an id is an update, or a confirm when nothing changed;
- a proposed Claim without an id confirms an identical existing Claim or is
  added as new;
- `retractedClaimIds` are removed. Retracting an id that no page owns is
  tolerated so retries stay safe; retracting another page's Claim is not;
- existing Claims the proposal omits are kept if issue-free, but an omitted
  Claim with a stale or unresolved issue rejects the whole proposal;
- a factual page may not end with zero Claims.

A successful mutation marks only that page dirty and clears the issues of the
Claims it targeted.

## Durability

`Session.Finalize` writes only dirty pages, and only when two gates pass: the
page has no remaining evidence debt, and every evidence entry still resolves
to the accepted version one final time. A page with Claims gets a
`verification` event. Sidecars of orphaned, vanished, or deleted pages are
removed.

Pages in the excluded set are left untouched; during a run that means the
other pending pages. Recoverable per-page failures become warnings, and
`Runtime.Finalize` turns any warning into an error. Work already persisted
stays persisted.

`AssertPageDurable` then re-reads the sidecar from disk and requires:

- `pageVersion` equals the hash of the page's current bytes;
- a verification event exists and is projected into the page's `verified`
  front matter;
- the persisted Claims match the session's Claims exactly, by id, statement,
  and evidence set.

`AssertWikiDurable` additionally requires that no sidecar outlives its page.

## Projection into OKF front matter

- `SyncSources` collapses each page's evidence to whole-file resources and
  writes them as OKF `sources` entries with deterministic ids
  `openwiki-source-<24 hex of sha256(resource)>`. That is the same scheme as
  upstream, so each tool reconciles the other's entries. Entries authored by
  anyone else are kept first and never duplicated.
- `SyncVerification` keeps `verified` events by other actors and replaces
  machine events (`owcli/*` and upstream `openwiki/*`) with at most one active
  event. Excluded pages are skipped entirely.
- Because projection changes page bytes, `RefreshPageVersions` rehashes
  sidecars afterwards. A page whose sidecar cannot be refreshed has its new
  stamp rolled back, so a stamp never outlives an accurate `pageVersion`.

See [OKF Front Matter and Finalization](okf-output.md) for the line-preserving
editors these projections use, and
[Generation Run Lifecycle](../workflows/generation-run.md) for when each step
runs.
