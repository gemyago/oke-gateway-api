<!-- AGENTS.md — instructions for coding agents. Nearest file in the tree wins. Keep this short, concrete, and current. -->

## Overview

This repo implements Gateway API support for Oracle Kubernetes Engine (OKE). It is a Go codebase with:
- the controller binary in `cmd/controller`
- a simple echo/example server in `cmd/server`
- main application code in `internal`
- container build tooling in `build`
- Helm/deployment assets in `deploy`

Go version is defined in `go.mod` and is the source of truth.

## Precedence And Expectations

- Follow the nearest `AGENTS.md` if more scoped files are added later.
- Treat this file as living documentation; update it when commands, workflows, or repo conventions change.
- Prefer project conventions over generic agent habits.

## Setup

- `direnv allow`
- `go mod download`
- `go install tool`
- Python tooling is optional unless you touch `build/scripts`; if needed, create `.venv` and install `requirements.txt`

Environment notes:
- `.envrc` sets `GOPATH` under `../go/<go-version>` and adds local `bin` to `PATH`
- optional local overrides live in `.envrc.local`

**Important**: Due to harness shell configuration, project related shell commands should be run with `direnv exec . <command>`.

## Common Commands

- Lint: `make lint`
- Tests: `make test`
- Coverage HTML output: `.cover/coverage.html`
- Run only one test: `go test ./... -run '^TestName$'`
- Run one package verbosely: `go test -v ./internal/...`
- Auto-fix some lint issues: `bin/golangci-lint run --fix`

If you touch build scripts:
- Build-script tests: `make -C build test`

If you touch deployment assets:
- Install Helm locally: `make -C deploy tools`

Release versioning:
- Helm chart `version` tracks the controller release without a leading `v`.
- Helm chart `appVersion` tracks the controller release tag with the leading `v`.

## Safe Local Runs

Use `--noop` for startup checks unless the task explicitly requires real Kubernetes or OCI calls.

- Controller dry-run: `go run ./cmd/controller start --noop`
- Example server dry-run: `go run ./cmd/server start --noop`

Without `--noop`, the controller may try to talk to Kubernetes and OCI using local credentials.

## Configuration

- Embedded config files live in `internal/config/*.json`
- Load order is `default.json` -> `<env>.json` -> optional `<env>-user.json`
- Default env is `local`
- Env var prefix is `APP_`
- Config keys map to env vars by replacing `.` and `-` with `_`

Examples:
- `APP_ENV=local`
- `APP_DEFAULT_LOG_LEVEL=DEBUG`
- `APP_JSON_LOGS=true`
- `APP_K8SAPI_NOOP=true`
- `APP_OCIAPI_NOOP=true`

## Repo Map

- `cmd/controller`: main OKE Gateway API controller
- `cmd/server`: simple HTTP echo server used for examples/testing
- `internal/api`: HTTP API wiring for the example server
- `internal/k8s`: controller manager startup and reconciliation wiring
- `internal/services/k8sapi`: Kubernetes access layer
- `internal/services/ociapi`: OCI access layer
- `build`: multi-platform binaries, Docker image builds, release artifacts
- `deploy`: Helm chart and example manifests
- `docs/https.md`: HTTPS-specific behavior

## Coding Conventions

- Keep changes idiomatic and gofmt-compatible
- Linting is strict; avoid adding `//nolint` unless it is justified
- Wrap errors with context, usually `fmt.Errorf("...: %w", err)`
- Do not hardcode secrets, tenancy data, kubeconfigs, or OCI credentials

## References

- Project overview: `README.md`
- Build details: `build/README.md`
- Deploy details: `deploy/README.md`
- HTTPS notes: `docs/https.md`

## Task Completion Protocol

### Coding Task Completion Protocol

Apply this when any Go, YAML, config, or other code-related files changed.

Always do all of the following before reporting completion:
1. Run `direnv exec . make lint` and confirm no errors.
2. Run `direnv exec . make test` and confirm all tests pass.
3. Update this file if commands, workflows, or architecture changed.

Report completion status:
- Lint: ✓ no errors
- Tests: ✓ all passing, coverage XX.XX%
- AGENTS.md: ✓ updated / no changes needed

### Non-Coding Task Completion Protocol

For investigation or documentation-only work:
- Summarize findings or actions taken.
- Confirm any deliverables produced.
