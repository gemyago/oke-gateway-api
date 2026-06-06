# E2E Bootstrap

This directory is a standalone Go module for live end-to-end testing of the OKE Gateway API
controller.

## Scope

- Keep e2e code isolated from controller production code.
- Do not import root repo `internal/...` packages from this module.
- Keep live e2e execution opt-in and separate from the root `make test` workflow.
- Assume the controller binary already exists before running live e2e tests.

## Local Environment

Put developer-specific live values in `e2e/.envrc.local`. That file is intentionally ignored.

Common live inputs:

- `OKE_E2E_LOAD_BALANCER_ID`
- `KUBECONFIG`
- `OCI_CONFIG_FILE` or `OCI_CLI_CONFIG_FILE`
- `OCI_CLI_PROFILE` or `OCI_CLI_CONFIG_PROFILE`

Safe defaults come from `e2e/.envrc`, including:

- `OKE_E2E_NAMESPACE_PREFIX=oke-gw-e2e-`
- `OKE_E2E_GATEWAY_CLASS_NAME=oke-gateway-api-e2e`
- `OKE_E2E_HTTP_PORT=80`
- `OKE_E2E_CONTROLLER_BIN=../dist/bin/controller`
- `OKE_E2E_SKIP_CONTROLLER_START=false`

## Commands

Run e2e commands from the repo root via `direnv exec .`:

```sh
direnv exec . make -C e2e lint
direnv exec . make -C e2e test
direnv exec . make -C e2e compile
direnv exec . make -C e2e cleanup
```

These Make targets load `e2e/.envrc` in the `e2e/` working directory, so its safe defaults and
ignored `e2e/.envrc.local` live overrides are applied even when the command starts from the repo
root.

The bootstrap currently provides:

- local linting via the root-pinned `../bin/golangci-lint`,
- local `go test` execution for e2e-owned packages,
- compile-only checks that do not require live infrastructure,
- a placeholder cleanup command entry point for future implementation.

## Controller Binary

Live e2e must not build the controller binary as part of the test target. Build it explicitly from
the repo root when needed:

```sh
direnv exec . make dist/bin
```
