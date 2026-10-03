# owcli

Generate, ground, and search a repository wiki for coding agents.

owcli is a clean-room Go implementation of
[OpenWiki](https://github.com/langchain-ai/openwiki). It keeps an
engineering wiki in `openwiki/` whose statements are backed by **Claims**:
`repo://` evidence that owcli rechecks against the source, so a page that
has drifted from the code is reported instead of trusted.

A single Go binary; no runtime dependencies.

## Install

```sh
go install github.com/Hoodoo/owcli/cmd/owcli@latest
# or, from a clone:
make install          # builds and installs ~/.local/bin/owcli
```

## Write a wiki

There are two ways:

- **Your coding agent writes it** (no API key). Run `owcli agents-md` once
  to add routing instructions to `AGENTS.md`, then ask the agent to
  initialize or update the wiki. It follows `owcli quickstart` and drives
  `owcli run begin|plan|next|submit|finish`.
- **owcli writes it** with its own model calls: `owcli init` and
  `owcli update` (needs `ANTHROPIC_API_KEY` by default; see `--help`).

To keep the wiki out of a repository you do not own, register it as
external first: `owcli bind --external`.

## Read and check

These never call a model:

```sh
owcli search "how are claims rechecked"     # sections across the wiki or workspace
owcli read <ref>                            # print a section
owcli check                                 # Claims, front matter, links, diagrams; exit 0 = current
owcli status                                # binding, last update, pending run, claim health
owcli serve                                 # local viewer: graph, pages, search, Claims
```

## Workspaces

Group related repositories so a search from any of them covers all their
wikis:

```sh
owcli workspace create shop ~/src/shop-api ~/src/shop-web
owcli wikis                                 # every wiki and workspace owcli knows
```

## Upstream OpenWiki

Do not wire upstream OpenWiki's skill or MCP server into the same agents:
they confuse it with owcli. `make openwiki-install` / `make
openwiki-uninstall` switch it for parity testing; see `AGENTS.md`.

## Development

```sh
make check   # go vet + go test
```

Design and the reasoning behind Claims: [docs/design.md](docs/design.md).
The repository's own wiki is in [openwiki/](openwiki/).

## License

MIT
