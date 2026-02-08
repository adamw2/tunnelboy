# TunnelBoy Makefile

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build build-all clean test install install-dev uninstall-dev run lint help

# Default target
all: build

## build: Build for current platform
build:
	go build -ldflags "$(LDFLAGS)" -o bin/tunnelboy ./cmd/tunnelboy

## build-darwin-amd64: Build for macOS Intel
build-darwin-amd64:
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/tunnelboy_darwin_amd64 ./cmd/tunnelboy

## build-darwin-arm64: Build for macOS Apple Silicon
build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/tunnelboy_darwin_arm64 ./cmd/tunnelboy

## build-all: Build for all macOS architectures
build-all: build-darwin-amd64 build-darwin-arm64

## clean: Remove build artifacts
clean:
	rm -rf bin/
	rm -f tunnelboy

## test: Run tests
test:
	go test -v ./...

## test-coverage: Run tests with coverage
test-coverage:
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

## install: Install to GOPATH/bin
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/tunnelboy

## install-dev: Create development symlink in /usr/local/bin
install-dev: build
	@echo "Creating development symlink..."
	@sudo ln -sf $(CURDIR)/bin/tunnelboy /usr/local/bin/tunnelboy
	@echo "✓ Symlink created: /usr/local/bin/tunnelboy -> $(CURDIR)/bin/tunnelboy"
	@echo ""
	@echo "Note: Remove symlink with 'make uninstall-dev' before installing via Homebrew"

## uninstall-dev: Remove development symlink
uninstall-dev:
	@if [ -L /usr/local/bin/tunnelboy ]; then \
		sudo rm -f /usr/local/bin/tunnelboy; \
		echo "✓ Development symlink removed"; \
	else \
		echo "No development symlink found at /usr/local/bin/tunnelboy"; \
	fi

## run: Build and run
run: build
	./bin/tunnelboy

## lint: Run linter
lint:
	golangci-lint run

## deps: Download dependencies
deps:
	go mod download
	go mod tidy

## release: Create release archives
release: build-all
	mkdir -p dist
	cd bin && tar -czvf ../dist/tunnelboy_darwin_amd64.tar.gz tunnelboy_darwin_amd64
	cd bin && tar -czvf ../dist/tunnelboy_darwin_arm64.tar.gz tunnelboy_darwin_arm64
	cd dist && sha256sum *.tar.gz > checksums.txt

## help: Show this help
help:
	@echo "TunnelBoy - AWS VPC Tunneling CLI"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
