# freellm-router — Makefile
# Targets: build, test, lint, clean, install, release

BINARY      ?= freellm-router
PKG         := github.com/silverx/freellm-router
CMD         := ./cmd/freellm-router
VERSION     := $(shell git describe --tags --always 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
  -X 'main.version=$(VERSION)' \
  -X 'main.commit=$(COMMIT)' \
  -X 'main.date=$(DATE)'

.PHONY: all
all: build

.PHONY: build
build: ## Build the binary
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)
	@echo "Built ./$(BINARY) (v$(VERSION))"

.PHONY: test
test: ## Run all tests
	go test -race -count=1 ./...

.PHONY: test-verbose
test-verbose: ## Run tests with verbose output
	go test -race -count=1 -v ./...

.PHONY: lint
lint: ## Run go vet + (optional) golangci-lint
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not found — skipping (go vet passed)"; \
	fi

.PHONY: fmt
fmt: ## Format all Go files
	gofmt -w .
	@echo "Formatted."

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY)
	rm -rf dist/

.PHONY: install
install: ## Install to GOPATH/bin or /usr/local/bin
	go install -ldflags "$(LDFLAGS)" $(CMD)
	@echo "Installed freellm-router to $(shell go env GOPATH)/bin"

.PHONY: release
release: ## Cross-compile release binaries into dist/
	@echo "Building release binaries…"
	@mkdir -p dist/linux-amd64 dist/linux-arm64 dist/darwin-amd64 dist/darwin-arm64 dist/windows-amd64
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/linux-amd64/$(BINARY)   $(CMD)
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/linux-arm64/$(BINARY)   $(CMD)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/darwin-amd64/$(BINARY) $(CMD)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/darwin-arm64/$(BINARY) $(CMD)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/windows-amd64/$(BINARY).exe $(CMD)
	@echo "Done. Binaries in ./dist/"

.PHONY: help
help: ## Show this help
	@grep -E '^  [a-z-]+:' $(MAKEFILE_LIST) | sed 's/:.*##//' | column -t
