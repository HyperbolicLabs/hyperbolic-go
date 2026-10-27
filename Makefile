## Tool versions. Bump them here, and CI reads the same versions.
GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION ?= v1.9.0

LOCALBIN ?= $(CURDIR)/bin
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)

.DEFAULT_GOAL := help

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-17s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: test
test: ## Run the tests with the race detector.
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Print the test coverage.
	go test -count=1 -coverprofile=coverage.out -coverpkg=./... ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint.
	$(GOLANGCI_LINT) run ./...

.PHONY: vuln
vuln: ## Scan for known vulnerabilities (govulncheck).
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: fmt
fmt: ## Format the code.
	gofmt -w .

# Installs golangci-lint at the pinned version, named by version so a bump
# installs the new one instead of reusing a stale binary.
$(GOLANGCI_LINT):
	mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(LOCALBIN)/golangci-lint $(GOLANGCI_LINT)
