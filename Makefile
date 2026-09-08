.PHONY: all build test check lint fmt web-build web-test web-install clean installer-test package-release push release

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

test: web-test
	GOCACHE=$(GOCACHE) go test ./...

check: lint test web-build installer-test
	GOCACHE=$(GOCACHE) go vet ./...

lint:
	GOCACHE=$(GOCACHE) golangci-lint run ./...

fmt:
	gofmt -w cmd internal

web-install:
	npm --prefix web ci --no-audit --no-fund

web-build:
	npm --prefix web run build

web-test:
	npm --prefix web test

installer-test:
	python3 -m unittest discover -s scripts/tests -v

package-release:
	sh scripts/package-release.sh "$(VERSION)"

push:
	sh scripts/push.sh

release:
	sh scripts/release.sh "$(VERSION)"

clean:
	rm -rf bin web/dist
