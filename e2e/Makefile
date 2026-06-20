SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c

.PHONY: lint test preflight run-e2e-tests compile infra-cleanup

GOLANGCI_LINT ?= ../bin/golangci-lint

lint:
	@test -x "$(GOLANGCI_LINT)" || { \
		echo "missing $(GOLANGCI_LINT); build the root-pinned linter first"; \
		exit 1; \
	}
	$(GOLANGCI_LINT) run ./...

test:
	go test -count=1 ./internal/...

preflight:
	go run ./internal/cmd/e2e-check

run-e2e-tests:
	@$(MAKE) -C .. dist/bin
	@$(MAKE) preflight
	go test -v -count=1 -timeout 30m .

compile:
	go test -run '^$$' ./...

infra-cleanup:
	go run ./internal/cmd/e2e-cleanup
