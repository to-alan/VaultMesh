.PHONY: all build test check fmt web-build web-install clean

GOCACHE ?= /tmp/vaultmesh-go-cache
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -s -w -X github.com/to-alan/vaultmesh/internal/version.Version=$(VERSION) -X github.com/to-alan/vaultmesh/internal/version.Commit=$(COMMIT) -X github.com/to-alan/vaultmesh/internal/version.Date=$(DATE)

all: check build

build: web-build
	mkdir -p bin
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/vaultmesh-server ./cmd/vaultmesh-server
	GOCACHE=$(GOCACHE) CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/vaultmesh-agent ./cmd/vaultmesh-agent

test:
	GOCACHE=$(GOCACHE) go test ./...

check: test web-build
	GOCACHE=$(GOCACHE) go vet ./...

fmt:
	gofmt -w cmd internal

web-install:
	npm --prefix web ci --no-audit --no-fund

web-build:
	npm --prefix web run build

clean:
	rm -rf bin web/dist
