VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -X owcli/internal/version.Version=$(VERSION)
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: build test vet check install update

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
