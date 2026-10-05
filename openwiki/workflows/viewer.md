---
type: Workflow
title: Browser Viewer
description: How owcli serve shows wikis in a browser - the loopback server and its default scope, the JSON API, how the page graph is built, page rendering with Claims, the embedded UI and its navigation rules, and how the viewer is tested.
tags: [viewer, serve, graph, ui]
verified:
  - by: owcli/v0.3.0-1-g9e64744
    at: "2026-10-05T09:15:10.665Z"
sources:
  - id: openwiki-source-0542b60281e3aea77c59392e
    resource: repo://docs/design.md
  - id: openwiki-source-624bec8caa72beb0cfc3a9ff
    resource: repo://internal/cli/serve.go
  - id: openwiki-source-57729b5108315c929e78b3e6
    resource: repo://internal/okf/graph.go
  - id: openwiki-source-a6c304c8e3ebd6b58ffdfb5a
    resource: repo://internal/serve/listen.go
  - id: openwiki-source-64833f7254658ff68acd9053
    resource: repo://internal/serve/serve.go
  - id: openwiki-source-00cfbd2eac182b4780e3db1b
    resource: repo://internal/serve/serve_test.go
  - id: openwiki-source-8c0d5d551a6c5db87a710545
    resource: repo://internal/serve/static/app.js
generated: { by: "owcli/v0.3.0-1-g9e64744", at: "2026-10-05T09:15:10.885Z" }
---

# Browser Viewer

`owcli serve` starts a read-only viewer for people: a graph of a wiki's pages
and links beside a reader that renders each page with its Claims, plus
search. It follows upstream's `openwiki visualize` (a node graph next to a
Markdown reader) and adds what owcli knows: workspaces, Claim health, and
every wiki in the registries. It lives in the same binary because everything
it shows comes from internal packages (`store`, `okf`, `claims`, `search`)
that a separate program could not import.

## Server

`internal/cli/serve.go` builds the server and `internal/serve` implements it.

- By default it listens on `127.0.0.1`. The port defaults to 4321; when it is
  taken, `Listen` tries the next ports. `--addr host:port` listens on exactly
  that address instead (with a warning when it is not loopback and no
  `--user-header` is set); giving both `--addr` and `--port` is an error. It
  prints the URL and opens the browser unless `--no-open`, then runs until
  Ctrl-C.
- Every request passes a Host check first: the `Host` header must be
  `localhost`, a loopback IP, the listen host, or a name given with
  `--allow-host` (403 otherwise), which keeps other web pages from reading
  the wikis by DNS rebinding.
- Only GET is accepted. There is no file watching: every request reads the
  wiki files again, so reloading the page shows edits.
- The default scope is what `owcli search` would use in the directory serve
  started in: the repository's own wiki or its workspace. That wiki does not
  need to be registered; if it has no ID it gets its `WikiID`. Outside a
  repository the default is empty and the UI opens the first known wiki or
  workspace. `-C <path>` picks another starting repository.

## API

| Endpoint | Returns |
| --- | --- |
| `/api/config` | the default scope (workspace and wiki IDs) and the producer version |
| `/api/wikis` | the `owcli wikis` listing |
| `/api/graph?wiki=` or `?workspace=` | nodes and edges for one wiki or a whole workspace, plus skipped members |
| `/api/page?wiki=&page=` | front matter, rendered HTML, Claims with evidence and preflight issues, links, backlinks |
| `/api/search?q=&wiki=` or `&workspace=` | the search JSON; every result carries its `wiki` |

Without `wiki` or `workspace`, a request uses the default scope. Wiki IDs
resolve against the default scope first, then the registries (see
[Workspaces](../concepts/workspaces.md#referring-to-a-wiki-from-anywhere)).
Errors are `{"error": {"message"}}` with status 400 for bad references and
404 for a page that does not exist. A page path is normalized and must be one
of the wiki's concept pages, so a request cannot reach other files.

## The graph

Each concept page is a node. Its ID is `<wiki>:<path without .md>`, so pages
of different wikis never collide. A node carries the page's title, type,
description, tags, body length (which sizes the circle), its outgoing links
and backlinks, its Claim count, and how many of its Claims are stale or
unresolved.

Edges come from `okf.PageLinks`, which reads a page's Markdown links and
keeps only links to other concept pages of the same wiki. Images, external
URLs, links to source files or directories, links to missing pages, and
links to the page itself are dropped. Links never cross wikis, so in a
workspace the graph shows one cluster per wiki.

Claim counts come from running the Claims preflight over every page each time
the graph is requested; pages with stale or unresolved Claims are drawn with
a red ring.

## Pages

`/api/page` renders the page body with goldmark and GitHub-flavoured
Markdown. Raw HTML in a page is not passed through, so a page cannot inject
markup. Heading IDs use the same GitHub-style slugs as wiki links and search
refs (`okf.Slug` with `-N` suffixes), so `#anchor` links and search results
land on the right section. Mermaid fences stay as code blocks for the browser
to draw.

The page's Claims come with their evidence resources and, for any Claim the
preflight flags, the issue kind and the resources that changed.

## The UI

The UI is three embedded files (`index.html`, `app.js`, `style.css`) with no
build step and no dependencies. Only Mermaid is fetched from a CDN, and only
when a page has a diagram; offline, the diagram shows as its source.

- **Header:** a picker of every workspace and wiki from `/api/wikis`,
  search with keyboard navigation, colouring by page type or by wiki, and
  **Pages** and **Graph** toggles that hide the sidebar or the graph. The
  choices are kept per browser in `localStorage`; when storage is blocked the
  toggles still work, with defaults on the next visit.
- **Sidebar:** the scope's pages as a tree, grouped by wiki in a workspace
  and then by directory, with root pages first and pages sorted by title. A
  filter matches titles and paths. The open page is highlighted and scrolled
  into view, and pages with Claims to recheck carry a red dot. On narrow
  screens the sidebar is a drawer, closed by default, that closes again
  after a page is picked.
- **Graph:** a small force-directed layout on a canvas, computed only once
  the graph is shown. Drag the background
  to pan, scroll to zoom, drag a node to move it, click a node to read it.
  Hovering shows a tooltip and highlights the node's neighbours. The view is
  refitted when the window resizes, until the reader pans or zooms.
- **Reader:** the page's title, description, type, tags, and Claim health,
  then the rendered page. Links to other pages open inside the viewer (with
  their anchor), links to source files are shown but not followed, and
  external links open in a new tab. Below the page are its Claims (opened
  automatically when some need rechecking) and its links and backlinks.
- **URL:** the scope and page live in the URL hash, so the back button works
  and a link to the viewer opens the same page.

Navigation is sequenced: every page request gets a number, and a response
for a page that is no longer the latest request is dropped, so a slow answer
never replaces a newer page. Mermaid diagrams are drawn off the page with
`mermaid.render`, one at a time, and an SVG is inserted only if its page is
still shown, so navigating away mid-render leaves nothing half-drawn.

## Behind a reverse proxy

`serve.Options` carries `ListenHost`, `AllowHosts`, and `UserHeader`, set
from `--addr`/`--port`, `--allow-host`, and `--user-header`. With a user
header (`X-Goog-Authenticated-User-Email` behind Google IAP), a request
without it gets 401 before routing, so traffic that bypasses the proxy fails
closed; `Server.Viewer` strips IAP's `accounts.google.com:` prefix. The
header is only as trustworthy as the network, so it is meant for an address
only the proxy can reach. The viewer is read-only, so the user is not
recorded anywhere.

## Testing

`internal/serve/serve_test.go` covers the API against two temporary Git
repositories in a workspace:

- graphs for the default scope, one wiki, and a workspace, plus bad
  references;
- page rendering: anchors, the Mermaid fence, raw HTML dropped, traversal and
  missing pages returning 404;
- workspace search, the config, POST being refused, and the embedded UI
  files being served;
- loopback listening and skipping a busy port;
- the Host check and running behind a proxy (`TestHostGuardAndProxy`).

The browser behaviour (graph interaction, navigation, Mermaid, layouts) was
checked by driving the UI in Chrome with Playwright during development; that
script is not part of the repository. See [Testing](../testing/overview.md)
for the rest of the suite.
