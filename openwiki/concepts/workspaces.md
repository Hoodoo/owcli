---
type: Concept
title: Workspaces
description: How owcli groups repository wikis into named workspaces so an agent in one repository can search and read the others - the registry and its ID rules, member resolution, search-scope selection, federated search, the workspace commands, and where owcli differs from upstream.
tags: [workspaces, search, registry, cli]
verified:
  - by: owcli/0d035b9
    at: "2026-10-02T08:30:28.060Z"
sources:
  - id: openwiki-source-86bf4030244551d79539af25
    resource: repo://internal/cli/workspace.go
  - id: openwiki-source-0eff31b693285d0d2305a525
    resource: repo://internal/search/compat_test.go
  - id: openwiki-source-2f97c192c632b2ecd0cb2b99
    resource: repo://internal/store/scope.go
  - id: openwiki-source-2697fb4bf3ba0749710fbc98
    resource: repo://internal/store/wikiref.go
  - id: openwiki-source-40f5deaf3e3708af3d62370d
    resource: repo://internal/store/workspaces.go
generated: { by: "owcli/0d035b9", at: "2026-10-02T08:30:45.958Z" }
---

# Workspaces

A workspace is a named set of repository wikis. From any member repository,
`owcli search` covers every wiki in the workspace in one ranking, and each
result names the wiki it came from so `owcli read --wiki` can open it. A
typical use is a knowledge base: a well-documented library or reference
project grouped with the new repositories built in its style.

The model follows upstream's `openwiki link`, but the registry, the
commands, and some rules are owcli's own (see
[Differences from upstream](#differences-from-upstream)). The code is in
`internal/store/workspaces.go` (registry), `internal/store/scope.go` (scope
rules), `internal/search` (ranking across wikis, see
[Wiki Search and Read](search.md)), and `internal/cli/workspace.go`
(commands).

## The registry

The registry is `$XDG_CONFIG_HOME/owcli/workspaces.json`, next to the
binding registry described in
[Storage Layouts and Bindings](../architecture/storage-and-bindings.md). It
uses upstream's version-1 schema:

- `wikis`: `{id, name, root}`, one entry per member repository, keyed by its
  canonical Git root;
- `workspaces`: `{id, name, wikis}`, with members listed by wiki ID;
- `active`: `{wiki, workspace}`, a repository's chosen workspace when it is in
  several.

Membership is many-to-many. Loading is strict: unknown fields, another
version, duplicate IDs or roots, names that clash ignoring case, members that
are not registered, and active selections that point outside their workspace
are all rejected as invalid state.

Edits never patch the file. `SaveWorkspaces` takes the complete list of
workspaces as drafts (ID, name, member roots) and replaces the registry
atomically:

- A wiki keeps its ID while its root stays a member somewhere; a workspace
  keeps its ID while a draft carries it. Wikis no workspace lists any more
  are dropped from the inventory.
- New IDs are slugs of the name (the repository's directory name for a wiki):
  lowercase, other characters collapsed to `-`, at most 56 characters, made
  unique with `-2`, `-3`, ... IDs that survive are reserved first, so a new
  entry never takes one.
- Names are trimmed, at most 80 characters, and unique ignoring case.
- An active selection the edit invalidates is removed.

Lookups accept a workspace ID or its name in any case.

## Members and their wikis

The registry stores repositories, not wiki paths. `ResolveMembers` finds
each member's wiki through the normal binding resolution, so in-repo,
external, and `--wiki-dir` wikis all work. A member that cannot be searched
is reported with a reason and skipped, without failing the workspace:

- the repository is gone;
- it has no wiki (unbound and no `openwiki/` directory);
- its wiki directory is missing.

A member does not need a wiki of its own to search its workspace. A new,
still empty repository can join a knowledge base and use it from day one.

## Choosing what a search covers

`ResolveSearchScope` decides from the current repository:

1. An explicit `--workspace` (ID or name) is used, but only if it contains
   the repository.
2. A repository in no workspace searches its own wiki.
3. One in exactly one workspace searches that workspace.
4. One in several searches its active workspace.
5. One in several with no active selection gets `workspace_required` and the
   list of choices; `owcli search` prints them and exits 1. An agent should ask
   the user, then pass `--workspace` or have the user run
   `owcli workspace use`.

`ResolveReadableWiki` lets `owcli read --wiki <id>` open the repository's own
wiki or any wiki that shares a workspace with it, and nothing else.

These restrictions protect the current repository's view. When there is no
current wiki (the command runs outside any repository, or in one with no
wiki that is in no workspace), there is nothing to protect: an explicit
`--workspace` is searched directly, and `--wiki` names any known wiki. A
command with no target there fails as before.

## Referring to a wiki from anywhere

`ResolveWikiRef` turns a reference into a wiki using only the binding and
workspace registries, so it works from any directory:

- a workspace member's ID (`kb`);
- a wiki ID, `WikiID(root)`: the repository slug plus a hash of its root
  (`vui-workitem-6b04d4025b7d`), shown by `owcli bindings`;
- a repository name, when exactly one bound repository or member has it;
  otherwise the error lists the matching IDs.

Implicit in-repo wikis that are neither bound nor members are unknown to the
registries; reach them by path with `-C`. `status` and `check` accept
`--wiki` from anywhere. Writing commands (`init`, `update`, `run`) never
resolve by reference: they work on a checkout, named with `-C`.

## Commands

`owcli workspace` manages the registry without a model and without touching
any wiki:

| Command | Effect |
| --- | --- |
| `create <name> [repo...]` | new workspace, optionally with members; fails if the name exists |
| `add <workspace> <repo...>` | add members (duplicates collapse) |
| `remove <workspace> <repo...>` | remove members; a path that is not a member is an error |
| `delete <workspace>` | delete the workspace; member wikis are untouched |
| `list` | every workspace with its members |
| `wikis <workspace>` | members, their wiki directories, and why any is not searchable |
| `use <workspace>` | set this repository's active workspace |
| `current` | this repository's workspaces, with the active one marked |
| `clear` | forget this repository's active workspace |

Repositories are given as paths and resolved to their Git roots. Output is
text by default. With `--json`, each command prints one object, and errors
print `{"error": {"code", "message"}}`: `invalid_input` for a wrong request
(unknown workspace, duplicate name, not a member), `not_found` for an unbound
repository.

## Differences from upstream

- **Own registry.** owcli never reads or writes upstream's
  `~/.openwiki/wiki-workspaces.json`, so the two tools keep separate
  workspaces even though the schema is the same.
- **Any binding.** Upstream only finds wikis stored in the repository;
  owcli resolves members through its bindings, so external wikis can join.
- **Members without a wiki** can search their workspaces; upstream requires
  each member to have one.
- **No finder TUI.** Upstream's `link` is an interactive repository picker;
  owcli's subcommands are scriptable so agents can drive them.

Federated ranking matches upstream's: with owcli's own wiki split across two
repositories in one workspace, both tools return the same wiki and ref at
every rank for all 18 compatibility queries (see
[Testing](../testing/overview.md)).
