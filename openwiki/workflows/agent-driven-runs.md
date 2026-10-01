---
type: Workflow
title: Agent-Driven Runs and Agent Instructions
description: How an interactive coding agent drives owcli init and update itself through the owcli run commands (JSON in and out, no model calls by owcli), how snapshots and resumption work across processes, and how owcli agents-md and owcli quickstart deliver Kata-style instructions.
tags: [agents, lifecycle, json, instructions, agents-md]
verified:
  - by: owcli/8cd3bda
    at: "2026-10-01T22:03:52.878Z"
sources:
  - id: openwiki-source-58776e6c955bcb51b8c7cf24
    resource: repo://cmd/owcli/main.go
  - id: openwiki-source-9e19352f1730fcdbfb2c88c1
    resource: repo://internal/cli/agentsmd.go
  - id: openwiki-source-7a349ce9e7059616884bc063
    resource: repo://internal/cli/host.go
  - id: openwiki-source-a8036dfe86a8391c911fe4a6
    resource: repo://internal/cli/instructions.go
  - id: openwiki-source-864ce28919ccb443e0ca864d
    resource: repo://internal/run/git.go
  - id: openwiki-source-3d2ada078788ef210e305606
    resource: repo://internal/run/host.go
  - id: openwiki-source-6353eac56e48b42f7340a5d5
    resource: repo://internal/run/run.go
generated: { by: "owcli/8cd3bda", at: "2026-10-01T22:04:05.268Z" }
---

# Agent-Driven Runs and Agent Instructions

owcli can generate a wiki with its own agents (see
[Generation Run Lifecycle](generation-run.md)), but the primary workflow is
an interactive coding agent doing the research and writing itself. In that
mode owcli calls no model. It keeps the run state, validates every page and
Claim, and finalizes the wiki, exactly as when its own agents write. The
`owcli run` command group exposes the lifecycle one step per command; the
agent learns the procedure from instructions modeled on Kata's.

## The run commands

| Command | Input | Output (JSON) |
| --- | --- | --- |
| `owcli run begin init\|update [--external] [--message M]` | — | `status`: `noop`, `planning`, or `generating`; `runId`, `resumed`, `planInvalidated`, existing `pages`, and for updates `changedPaths` and `claimIssues`; `INSTRUCTIONS.md` text |
| `owcli run plan --file F` | plan JSON from `F` (or stdin) | accepted page order |
| `owcli run next` | — | `pending` with `job` (`id`, `path`, `title`, `purpose`, `seedPaths`, `relatedPages`, `existing`, `existingClaimCount`, `claimsRequiringAttention`), the whole `plan`, and planner `instructions`; or `complete` |
| `owcli run inspect <jobId>` | — | every Claim the page owns, with ids |
| `owcli run submit <jobId> --file F` | sparse Claim decisions from `F` (or stdin) | `complete` with `remaining` |
| `owcli run skip <jobId>` | — | `skipped` |
| `owcli run finish` | — | `complete` or `interrupted`, skipped and deleted pages, link and metadata notes |

Every success carries a `next` hint naming the following step, so an agent
can follow the loop without remembering it. Failures print
`{"error": {"code", "message"}}` and exit 1:

- `invalid_input`: fix the page or the JSON and retry the same step;
- `invalid_state`: the step doesn't fit the run's state;
- `conflict`: an interrupted run of the other mode exists;
- `not_found`: no active run, or an unknown job.

## Passing JSON input

`plan` and `submit` read one JSON document from `--file`, or from stdin when
the flag is empty or `-`. The instructions, help text, and `next` hints lead
with `--file`: the agent writes the JSON with its own file tools and runs one
command per step. Agents that cannot pipe a heredoc otherwise open a terminal
session and type the payload, which costs two tool calls per step and echoes
the whole payload back into the agent's context. To keep that from happening
silently, a terminal on stdin is rejected at once with `invalid_input` and a
hint to use `--file`.

The file belongs outside the repository. Source fingerprints include
untracked, non-ignored files, so a `plan.json` left in the working tree looks
like a source change and `finish` reports the run as interrupted.

JSON input is decoded strictly, so a misspelled field is reported instead of
silently ignored. Evidence may be written as `"repo://..."` strings or as
upstream-style `{"resource": "repo://..."}` objects. Agent-driven runs record
`host-agent` as the model in `.last-update.json`.

## Statelessness and resumption

Each command is a separate process:

- `begin` creates or resumes the checkpoint; every other step reattaches with
  `run.Open`, which loads `.run.json` and rebuilds the Claims runtime from
  durable state without modifying anything.
- `next` persists the current job's pre-edit snapshot under
  `<wiki>/.run-snapshots/<jobId>.json`. Only the first capture is kept, so
  calling `next` again after editing never overwrites it. This is why an agent
  must call `next` before writing the page.
- `skip` restores the page and its sidecar from that saved snapshot.
- `finish` loads the snapshots of all skipped jobs, then removes the snapshot
  directory along with the checkpoint.

An interrupted session resumes by calling `owcli run begin` again in the same
mode: skipped jobs become pending, and if the source changed since planning,
the plan is dropped and `status` is `planning` again.

## When updates happen

The wiki documents the repository's default branch, and the default trigger
is a merge into it. Several merges can share one update run, and work
branches never churn the wiki. To support this, `owcli run begin` reports
`branch` and `defaultBranch` (origin's HEAD, else a local `main` or
`master`). When they differ, it adds a `warning` telling the agent to update
after merging unless the user asked otherwise.

For external wikis, `owcli run finish` also commits the wiki's files to their
own Git history and returns the commit as `wikiCommit`. See
[Storage Layouts and Bindings](../architecture/storage-and-bindings.md).

## Planning inputs for updates

`begin update` reports `changedPaths`: everything that differs from the last
documented commit. That covers commits since then plus uncommitted and
untracked files, with ignored and wiki paths left out, capped at 300. It also
reports how many stale and unresolved Claims each page has.

An update plan may list no pages at all: pages whose Claims cite changed or
missing source are added to the queue automatically, and each comes with its
`claimsRequiringAttention`.

## Instructions for agents

Following Kata's pattern, there are two levels:

- **`owcli agents-md`** writes a compact routing block into `AGENTS.md`
  (created if missing), and into an existing `CLAUDE.md` unless it only
  imports `AGENTS.md`, between managed `OPENWIKI:START/END` markers. Refreshing
  is idempotent, and the markers are upstream's, so the block replaces an
  upstream-generated one. The block says:
  - search just in time, read other wikis of a workspace with `--wiki`, and
    ask the user when search reports `workspace_required`;
  - never hand-edit the wiki;
  - pass plan and submit JSON with `--file`, from outside the repository;
  - after merging code into the default branch, run `owcli check` there, and
    if it fails, follow a dot digraph through begin, plan, next, write,
    submit (or skip), and finish;
  - the wiki documents the default branch, so it is not updated on work
    branches unless the user asks.

  For an external binding owcli refuses to write into the repository;
  `owcli agents-md --print` outputs the block for the agent's global
  instructions instead.
- **`owcli quickstart`** prints the full procedure: reading commands,
  workspaces (cross-wiki search and read, `workspace_required`, the
  `owcli workspace` commands), health checks, binding inventory, where registry and wiki files live, safe
  reattachment after a clone moves, each lifecycle step with example JSON,
  error codes, and the planning, page, and Claim standards.

Those standards are text constants in `internal/guide`. owcli's own planner
and worker prompts are composed from the same constants, so an interactive
agent and owcli's built-in agents are held to one standard.
