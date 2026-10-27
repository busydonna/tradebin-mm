.PHONY: help
help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the tradebin binary
	go build -o tradebin .

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint (linters + gofmt/goimports)
	golangci-lint run

.PHONY: fmt
fmt: ## Format the code
	golangci-lint fmt

.PHONY: tidy-check
tidy-check: ## Fail when go.mod/go.sum are not tidy
	go mod tidy -diff

.PHONY: vulncheck
vulncheck: ## govulncheck; fails on reachable vulnerabilities that have a fix
	scripts/vulncheck.sh

.PHONY: test
test: ## Unit tests (no network, every dependency is faked)
	go test ./...

.PHONY: check
check: build vet lint tidy-check vulncheck test ## Everything CI runs
