VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -X owcli/internal/version.Version=$(VERSION)
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: build test vet check install update openwiki-uninstall openwiki-install

build:
	go build -ldflags "$(LDFLAGS)" -o bin/owcli ./cmd/owcli

test:
	go test ./...

vet:
	go vet ./...

check: vet test

install: build
	install -d "$(BINDIR)"
	install -m 0755 bin/owcli "$(BINDIR)/owcli"
	@echo "installed $(BINDIR)/owcli ($(VERSION))"

# Pull the latest source, then reinstall. Refuses to merge diverged history.
update:
	git pull --ff-only
	$(MAKE) install

# Upstream OpenWiki, for the human operator (sudo). Agents confuse its skill
# and MCP server with owcli, so keep it uninstalled where agents work; install
# it to run the upstream parity tests or to check upstream behavior.
OPENWIKI_HOSTS ?= cursor claude

openwiki-uninstall:
	-pkill -f '[o]penwiki mcp' # brackets keep pkill from matching its own shell
	-for host in $(OPENWIKI_HOSTS); do openwiki integrations uninstall $$host; done
	sudo npm uninstall -g openwiki

openwiki-install:
	sudo npm install -g openwiki
	for host in $(OPENWIKI_HOSTS); do openwiki integrations install $$host; done
