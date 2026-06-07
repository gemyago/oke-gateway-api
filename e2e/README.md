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
- `KUBECONFIG` (optional; when unset, the default kubeconfig loading rules are used)
- `OCI_CONFIG_FILE` or `OCI_CLI_CONFIG_FILE`
- `OCI_CLI_PROFILE` or `OCI_CLI_CONFIG_PROFILE`

Safe defaults come from `e2e/.envrc`, including:

- `OKE_E2E_NAMESPACE_PREFIX=oke-gw-e2e-`
- `OKE_E2E_GATEWAY_CLASS_NAME=oke-gateway-api-e2e`
- `OKE_E2E_HTTP_PORT=80`
- `OKE_E2E_CONTROLLER_BIN=../dist/bin/controller`
- `OKE_E2E_SKIP_CONTROLLER_START=false`

**Env Check**:

```sh
direnv exec . oci lb load-balancer get --load-balancer-id ${OKE_E2E_LOAD_BALANCER_ID}
```
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

The e2e module currently provides:

- local linting via the root-pinned `../bin/golangci-lint`,
- local `go test` execution for e2e-owned packages,
- compile-only checks that do not require live infrastructure,
- an OCI cleanup command for operator-driven disposable load balancer resets,
- a controller process helper under `e2e/internal/controllerproc` that launches the prebuilt
  controller binary from `OKE_E2E_CONTROLLER_BIN`, forwards `KUBECONFIG` plus the caller OCI SDK
  env into the child process, forces `APP_K8SAPI_NOOP=false` and `APP_OCIAPI_NOOP=false`, streams
  controller stdout/stderr into test logs, and shuts the child down during test cleanup,
- Kubernetes fixture helpers under `e2e/internal/e2ek8s` for controller-runtime client creation,
  typed resource builders, unstructured `GatewayConfig` fixtures, readiness waiters, and
  namespace-prefix-scoped cleanup for shared clusters,
- HTTP probe helpers under `e2e/internal/probe` for polling `http://<public-ip>/<path>` and
  decoding the echo server JSON shape without importing root repo internals,
- a live `e2e/http_test.go` MVP that creates a unique namespace plus Gateway API resources, probes
  `/echo`, captures programmed OCI routing policy rule names from the `HTTPRoute` annotation,
  deletes the route, verifies `/echo` no longer serves the echo response, verifies the captured OCI
  rule names disappear from the listener routing policy, and leaves full disposable load balancer
  reset to the separate cleanup command.

## Cleanup Command

`direnv exec . make -C e2e cleanup` is an operator command for resetting disposable OCI load
balancer state outside the initial live test run.

Current cleanup behavior:

- builds an OCI load balancer client from the default SDK config flow, with optional config file and
  profile overrides from the documented OCI env vars,
- validates that `OKE_E2E_LOAD_BALANCER_ID` exists and that the load balancer has at least one
  public IP,
- picks a stable public IP from the load balancer response for later probe-oriented workflows,
- deletes listeners first, then routing policies, then backend sets,
- waits for the OCI work request after each successful mutation,
- does not delete the load balancer itself.

The cleanup command only needs OCI-related inputs. It does not require Kubernetes helper wiring or
controller process management.

For Kubernetes-side cleanup, the `e2e/internal/e2ek8s` helper only deletes namespaces whose names
start with the configured `OKE_E2E_NAMESPACE_PREFIX`.

## Controller Binary

Live e2e must not build the controller binary as part of the test target. Build it explicitly from
the repo root when needed:

```sh
direnv exec . make dist/bin
```

The e2e config loader validates that `OKE_E2E_CONTROLLER_BIN` points to an existing file before a
live workflow continues.

When `OKE_E2E_SKIP_CONTROLLER_START=true`, the helper skips child-process startup so a live test can
target an already running controller without requiring a local binary during offline verification.

If `OKE_E2E_LOAD_BALANCER_ID` is not set, `e2e/http_test.go` skips with a clear message instead of
attempting live infrastructure access during offline verification. When `KUBECONFIG` is unset, the
e2e helpers fall back to the default kubeconfig loading rules.
