BINARY   := concord
PKG      := ./cmd/concord
BIN_DIR  := bin
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build the binary into ./bin
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(PKG)

.PHONY: install
install: ## Install the binary into $GOBIN / $GOPATH/bin
	go install -ldflags "$(LDFLAGS)" $(PKG)

.PHONY: test
test: ## Run all tests
	go test ./...

.PHONY: cover
cover: ## Run tests with a coverage summary
	go test -cover ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format all Go files
	gofmt -w .

.PHONY: tidy
tidy: ## Sync go.mod / go.sum
	go mod tidy

.PHONY: check
check: fmt vet test ## Format, vet, and test

.PHONY: snapshot
snapshot: ## Build a local goreleaser snapshot (no publish)
	goreleaser build --snapshot --clean

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) dist
	go clean

.PHONY: help
help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
