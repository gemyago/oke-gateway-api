# E2E Bootstrap

This directory is a standalone Go module for live end-to-end testing of the OKE Gateway API
controller.

See also:

- `TEST_PLAN.md` for the prioritized live e2e scenario backlog.

## Scope

- Keep e2e code isolated from controller production code.
- Do not import root repo `internal/...` packages from this module.
- Keep live e2e execution opt-in and separate from the root `make test` workflow.
- Treat `make -C e2e test` as a support-only local check for e2e-owned helper packages.
- Use `make -C e2e preflight` for read-only live environment checks before the live path.
- Use `make -C e2e run-e2e-tests` as the explicit live entrypoint.

## Local Environment

Put developer-specific live values in `e2e/.envrc.local`. That file is intentionally ignored.

Common live inputs:

- `OKE_E2E_LOAD_BALANCER_ID`
- `OKE_E2E_KUBE_CONTEXT`
- `KUBECONFIG` (optional; when unset, the default kubeconfig loading rules are used)
- `OCI_CONFIG_FILE` or `OCI_CLI_CONFIG_FILE`
- `OCI_CLI_PROFILE` or `OCI_CLI_CONFIG_PROFILE`

Safe defaults come from `e2e/.envrc`, including:

- `OKE_E2E_NAMESPACE_PREFIX=oke-gw-e2e-`
- `OKE_E2E_GATEWAY_CLASS_NAME=oke-gateway-api-e2e`
- `OKE_E2E_HTTP_PORT=80`
- `OKE_E2E_CONTROLLER_BIN=../dist/bin/controller`
- `OKE_E2E_SKIP_CONTROLLER_START=false`

Required live config:

- `OKE_E2E_LOAD_BALANCER_ID` is required.
- `OKE_E2E_KUBE_CONTEXT` is required.
- `KUBECONFIG` is optional.
- The selected cluster must already have the `GatewayConfig` CRD installed.

## Preflight Checks

Before the live path, run:

```sh
direnv exec e2e make -C e2e preflight
```

The preflight command uses the same Go client paths as the live e2e helpers and checks:

- live config loads successfully,
- the controller binary exists unless `OKE_E2E_SKIP_CONTROLLER_START=true`,
- the configured Kubernetes context can read namespaces,
- the `gateway-configs.oke-gateway-api.gemyago.github.io` CRD exists,
- the configured OCI load balancer is reachable and has a public IP.

`direnv exec e2e make -C e2e run-e2e-tests` also runs this preflight automatically after it builds
the controller binary.

## Manual Troubleshooting Checks

If `preflight` fails and you want to isolate whether the problem is Kubernetes or OCI, these manual
commands are still useful.

**OCI manual check**:

```sh
oci lb load-balancer get --load-balancer-id ${OKE_E2E_LOAD_BALANCER_ID}
```

## Kubernetes Manual Checks

Confirm that the kubeconfig you intend to use contains the required context and that the cluster is
reachable.

When `KUBECONFIG` is set:

```sh
kubectl --kubeconfig "${KUBECONFIG}" config get-contexts
kubectl --kubeconfig "${KUBECONFIG}" --context "${OKE_E2E_KUBE_CONTEXT}" cluster-info
kubectl --kubeconfig "${KUBECONFIG}" --context "${OKE_E2E_KUBE_CONTEXT}" auth can-i get namespaces
```

When `KUBECONFIG` is unset and you want the default loading rules:

```sh
kubectl config get-contexts
kubectl --context "${OKE_E2E_KUBE_CONTEXT}" cluster-info
kubectl --context "${OKE_E2E_KUBE_CONTEXT}" auth can-i get namespaces
```

The live e2e client and the child controller both use the explicit `OKE_E2E_KUBE_CONTEXT`. If the
context is missing from the selected kubeconfig, the live run fails.

Confirm the CRD is present before the live path:

```sh
kubectl --context "${OKE_E2E_KUBE_CONTEXT}" get crd gateway-configs.oke-gateway-api.gemyago.github.io
```

If it is missing, install it explicitly before running the live test:

```sh
kubectl apply -f deploy/helm/controller/crds/gateway-config-crd.yaml
```

## Commands

Run e2e commands from the repo root:

```sh
direnv exec . make -C e2e lint
direnv exec . make -C e2e test
direnv exec e2e make -C e2e preflight
direnv exec . make -C e2e compile
direnv exec e2e make -C e2e run-e2e-tests
direnv exec . make -C e2e infra-cleanup
```

These Make targets load `e2e/.envrc` in the `e2e/` working directory. For the live preflight and
the live test path, prefer `direnv exec e2e ...` so the top-level direnv environment also matches
the e2e module.

The e2e module currently provides:

- local linting via the root-pinned `../bin/golangci-lint`,
- support-only local `go test` execution for e2e-owned helper packages under `make -C e2e test`,
- a read-only live preflight command under `make -C e2e preflight` that validates the configured
  controller binary, Kubernetes context, required `GatewayConfig` CRD, and disposable OCI load
  balancer using the same Go client paths as the live helpers,
- compile-only checks that do not require live infrastructure,
- an explicit live entrypoint under `make -C e2e run-e2e-tests`, which builds the controller first
  then runs the read-only preflight checks, and then runs `go test -count=1 .`,
- an operator cleanup command that removes e2e namespaces from the selected cluster and resets the
  disposable OCI load balancer,
- a controller process helper under `e2e/internal/controllerproc` that launches the prebuilt
  controller binary from `OKE_E2E_CONTROLLER_BIN`, shapes the selected kubeconfig down to
  `OKE_E2E_KUBE_CONTEXT` for the child controller, forwards `KUBECONFIG` plus the caller OCI SDK
  env into the child process, forces `APP_K8SAPI_NOOP=false` and `APP_OCIAPI_NOOP=false`, streams
  controller stdout/stderr into test logs, and shuts the child down during test cleanup,
- Kubernetes fixture helpers under `e2e/internal/e2ek8s` for controller-runtime client creation,
  typed resource builders, unstructured `GatewayConfig` fixtures, readiness waiters, and
  namespace-prefix-scoped cleanup for shared clusters, using the explicit
  `OKE_E2E_KUBE_CONTEXT` override with optional `KUBECONFIG`,
- HTTP probe helpers under `e2e/internal/probe` for polling `http://<public-ip>/<path>` and
  decoding the echo server JSON shape without importing root repo internals,
- a live `e2e/http_test.go` MVP that creates a unique namespace plus Gateway API resources, probes
  `/echo`, captures programmed OCI routing policy rule names from the `HTTPRoute` annotation,
  deletes the route, verifies `/echo` no longer serves the echo response, verifies the captured OCI
  rule names disappear from the listener routing policy, and leaves full disposable load balancer
  reset to the separate cleanup command.

## Infra Cleanup Command

`direnv exec . make -C e2e infra-cleanup` is an operator command for cleaning up e2e namespaces in
the selected cluster and resetting disposable OCI load balancer state outside the initial live test
run.

Current cleanup behavior:

- builds a Kubernetes controller-runtime client from `OKE_E2E_KUBE_CONTEXT` with optional
  `KUBECONFIG`, deletes namespaces whose names start with `OKE_E2E_NAMESPACE_PREFIX`, and waits for
  those namespaces to disappear so Kubernetes can finish removing the resources inside them,
- builds an OCI load balancer client from the default SDK config flow, with optional config file and
  profile overrides from the documented OCI env vars,
- validates that `OKE_E2E_LOAD_BALANCER_ID` exists and that the load balancer has at least one
  public IP,
- picks a stable public IP from the load balancer response for later probe-oriented workflows,
- deletes listeners first, then routing policies, then backend sets,
- waits for the OCI work request after each successful mutation,
- does not delete the load balancer itself.

The cleanup command requires both `OKE_E2E_LOAD_BALANCER_ID` and `OKE_E2E_KUBE_CONTEXT`. It does
not require controller process management, and it forces
`OKE_E2E_SKIP_CONTROLLER_START=true` internally so the controller binary is not needed for cleanup.

## Controller Binary

`direnv exec . make -C e2e run-e2e-tests` builds `../dist/bin/controller` before it starts the
live Go test run.

Build the controller explicitly from the repo root when you want that artifact ahead of time:

```sh
direnv exec . make dist/bin
```

Support-only targets such as `make -C e2e test` and `make -C e2e compile` do not build the
controller binary.

The live config loader validates that `OKE_E2E_CONTROLLER_BIN` points to an existing file before
the live workflow continues when `OKE_E2E_SKIP_CONTROLLER_START=false`.

When `OKE_E2E_SKIP_CONTROLLER_START=true`, the helper skips child-process startup so a live test can
target an already running controller without requiring a local binary during offline verification.

When `KUBECONFIG` is unset, the e2e helpers fall back to the default kubeconfig loading rules.
Both the e2e client and the child controller still require `OKE_E2E_KUBE_CONTEXT` and use that
explicit context.
