SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c

.PHONY: lint test run-e2e-tests compile infra-cleanup

GOLANGCI_LINT ?= ../bin/golangci-lint
DIRENV ?= direnv
E2E_ENV = eval "$$($(DIRENV) stdlib)"; source .envrc;

lint:
	@test -x "$(GOLANGCI_LINT)" || { \
		echo "missing $(GOLANGCI_LINT); build the root-pinned linter first"; \
		exit 1; \
	}
	@$(E2E_ENV) $(GOLANGCI_LINT) run ./...

test:
	@$(E2E_ENV) go test -count=1 ./internal/...

run-e2e-tests:
	@$(MAKE) -C .. dist/bin
	@$(E2E_ENV) go test -count=1 .

compile:
	@$(E2E_ENV) go test -run '^$$' ./...

infra-cleanup:
	@$(E2E_ENV) go run ./internal/cmd/e2e-cleanup
