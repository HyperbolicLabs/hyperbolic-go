## Tool versions. Bump them here, and CI reads the same versions.
GOLANGCI_LINT_VERSION ?= v2.15.0
GOVULNCHECK_VERSION ?= v1.9.0

# The published spec the SDK is generated from.
SPEC_URL ?= https://api.hyperbolic.ai/v2/openapi.json

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

# The generator (internal/gen) is a separate module that ./... does not reach,
# so it is linted here too.
.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint on the SDK and the generator.
	$(GOLANGCI_LINT) run ./...
	cd internal/gen && $(GOLANGCI_LINT) run ./...

.PHONY: vuln
vuln: ## Scan for known vulnerabilities (govulncheck).
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: fmt
fmt: ## Format the code.
	gofmt -w .

##@ Generation

.PHONY: generate
generate: ## Regenerate the SDK from openapi/ (spec, overlay.yaml, sdk.yaml).
	go -C internal/gen run . -root $(CURDIR)

.PHONY: verify-generated
verify-generated: generate ## Fail if the committed generated files are stale.
	@git diff --exit-code --stat -- . || { echo "Generated files are stale: run make generate and commit the result."; exit 1; }
	@test -z "$$(git status --porcelain --untracked-files=all -- '*_gen.go' '*_gen_test.go' testdata/responses)" || \
		{ git status --short -- '*_gen.go' '*_gen_test.go' testdata/responses; echo "make generate wrote new files: commit them."; exit 1; }

.PHONY: spec-sync
spec-sync: ## Pull the published spec into openapi/openapi.json and regenerate.
	go -C internal/gen run . -root $(CURDIR) -sync $(SPEC_URL)

# Installs golangci-lint at the pinned version, named by version so a bump
# installs the new one instead of reusing a stale binary.
$(GOLANGCI_LINT):
	mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(LOCALBIN)/golangci-lint $(GOLANGCI_LINT)
