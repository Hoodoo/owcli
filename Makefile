VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -X owcli/internal/version.Version=$(VERSION)

.PHONY: build test vet check

build:
	go build -ldflags "$(LDFLAGS)" -o bin/owcli ./cmd/owcli

test:
	go test ./...

vet:
	go vet ./...

check: vet test
