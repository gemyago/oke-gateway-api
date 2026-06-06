# HTTP E2E Test Design

This document describes the first end-to-end test slice for the OKE Gateway API controller. The
initial scope is HTTP only. HTTPS should be added later once the HTTP harness, cleanup, and
operator workflow are stable.

## Goals

- Exercise the real reconciliation path from Kubernetes Gateway API resources to OCI Load Balancer
  configuration.
- Verify real HTTP traffic reaches a Kubernetes backend through a pre-created disposable OCI Load
  Balancer.
- Keep e2e code isolated from controller production code.
- Make cleanup reliable enough for local iteration before the tests are polished.
- Reuse project practices where helpful by copying small patterns, not by importing root
  `internal/...` packages.

## Non-Goals

- Do not provision or delete the OCI Load Balancer itself.
- Do not test HTTPS in the first slice.
- Do not make e2e part of the default `make test` workflow.
- Do not import controller internals from the e2e project.
- Do not assume the target Kubernetes cluster is disposable.

## Proposed Layout

The e2e suite should be a nested Go project:

```text
e2e/
  AGENTS.md
  .envrc
  .envrc.local        # ignored, developer-specific live values
  .gitignore
  go.mod
  README.md
  Makefile
  http_test.go
  internal/
    cmd/
      e2e-cleanup/
        main.go
    config/
      env.go
    diag/
      slog.go
      attributes.go
    e2ek8s/
      client.go
      resources.go
      wait.go
    e2eoci/
      client.go
      cleanup.go
      wait.go
    probe/
      http.go
```

The `e2e/internal/...` layout keeps helper packages private to the e2e project. It also makes the
boundary clear: e2e may depend on public upstream packages, but not on root repo internals. The
manual cleanup command can live under `e2e/internal/cmd/e2e-cleanup` because it is only a local
operator entry point for this e2e module. It can still be executed with `go run
./internal/cmd/e2e-cleanup`.

`http_test.go` can sit at the e2e module root. Test files are compiled only by `go test`, and the
e2e module should not contain root-level non-test library code intended for import.

`e2e/AGENTS.md` should capture the local e2e rules: do not import root `internal/...`, live tests
are opt-in, the OCI Load Balancer is disposable, the Kubernetes cluster is shared, commands run via
`direnv exec .` from the repo root, and `.envrc.local` contains uncommitted developer-specific
values.

The nested module can use a module path such as:

```text
github.com/gemyago/oke-gateway-api/e2e
```

## Inputs

The HTTP MVP should require these environment variables:

```text
OKE_E2E_LOAD_BALANCER_ID
KUBECONFIG
```

OCI SDK configuration should use the same default mechanism as the controller:

```text
OCI_CONFIG_FILE
OCI_CLI_CONFIG_FILE
OCI_CLI_PROFILE
OCI_CLI_CONFIG_PROFILE
```

Optional inputs:

```text
OKE_E2E_NAMESPACE_PREFIX=oke-gw-e2e-
OKE_E2E_GATEWAY_CLASS_NAME=oke-gateway-api-e2e
OKE_E2E_HTTP_PORT=80
OKE_E2E_CONTROLLER_BIN=../dist/bin/controller
OKE_E2E_SKIP_CONTROLLER_START=false
```

The e2e project should derive the public IP from `GetLoadBalancer` using
`OKE_E2E_LOAD_BALANCER_ID`. If the load balancer has multiple public IP addresses, choose a stable
one by sorting the discovered public addresses and using the first. Fail clearly if no public IP is
present.

`OKE_E2E_CONTROLLER_BIN` defaults to the expected root build output. The test command should assume
the controller has already been built and fail fast if the binary is missing. Building the
controller is an explicit step outside `go test`, for example from the repo root:

```sh
direnv exec . make dist/bin
```

`OKE_E2E_SKIP_CONTROLLER_START=true` is useful only when a developer wants to run the tests against
an already-running controller. The default should be to start the current source tree's controller
as a child process.

## Env Files

The e2e module should include its own `.envrc` and an ignored `.envrc.local`.

`e2e/.envrc` should:

- load root environment defaults if needed,
- load `e2e/.envrc.local` when it exists,
- set safe non-secret defaults such as namespace prefix and controller binary path.

`e2e/.envrc.local` should not be committed. It is where developers put live environment values such
as `OKE_E2E_LOAD_BALANCER_ID`, `KUBECONFIG`, and OCI SDK config/profile variables.

## Runtime Model

The test should run the controller locally from the current source tree:

1. Verify the prebuilt controller binary exists at `OKE_E2E_CONTROLLER_BIN`.
2. Start the binary with the caller's `KUBECONFIG` and OCI SDK environment.
3. Run test resources in a unique namespace.
4. Stop the child process during test cleanup.

This avoids testing whatever controller image happens to be installed in the cluster.

The controller process should inherit normal configuration, with explicit safety values:

```text
APP_K8SAPI_NOOP=false
APP_OCIAPI_NOOP=false
```

## Kubernetes Client

Use `sigs.k8s.io/controller-runtime/pkg/client` with typed Gateway API resources:

- `sigs.k8s.io/gateway-api/apis/v1` for `GatewayClass`, `Gateway`, and `HTTPRoute`.
- Kubernetes core/apps/discovery APIs for namespace, deployment, service, pod, and endpoint
  readiness.

Use `unstructured.Unstructured` for `GatewayConfig` rather than importing root
`internal/types`. This keeps the e2e project independent from controller internals while still
creating the exact custom resource the controller expects:

```yaml
apiVersion: oke-gateway-api.gemyago.github.io/v1
kind: GatewayConfig
```

## Resource Definitions

Define Kubernetes resources in Go code for the MVP.

- Use typed objects for Gateway API and Kubernetes built-ins.
- Use a small code builder returning `unstructured.Unstructured` for `GatewayConfig`.
- Keep names, labels, and owner metadata generated from a single per-run fixture object.
- Keep YAML snippets in README/docs for humans, not as the source of truth for tests.

This keeps refactoring simple while test data is dynamic. If future scenarios need large static
fixtures, add YAML templates later and decode them through Kubernetes serializers instead of
manually parsing strings.

## OCI Client

Use the official OCI Go SDK:

```text
github.com/oracle/oci-go-sdk/v65/common
github.com/oracle/oci-go-sdk/v65/loadbalancer
```

The e2e project does not need to mirror every production interface. It should create the SDK client
directly, then hide repetitive polling and cleanup behind small e2e-local helpers.

Preflight should verify:

- The configured load balancer exists.
- At least one public IP address is present and can be selected for probes.
- The load balancer can be modified by the current OCI credentials.

## Disposable Load Balancer Cleanup

Assume the OCI Load Balancer is disposable. Cleanup should reset load balancer child resources
rather than trying to surgically identify a single run's resources.

The cleanup command should delete, in order:

1. Listeners.
2. Routing policies.
3. Backend sets.

Backend entries are removed as part of backend set deletion.

The command should not delete the load balancer itself.

Cleanup must wait for OCI work requests after every mutating call.

The manual command should be available before the first test is polished:

```sh
cd e2e
go run ./internal/cmd/e2e-cleanup --load-balancer-id "$OKE_E2E_LOAD_BALANCER_ID"
```

Because this assumes a disposable load balancer, the command should be explicit in its log output:

```text
Resetting disposable load balancer child resources
```

For Kubernetes cleanup, do not assume the cluster is disposable. Only delete namespaces matching the
configured e2e namespace prefix.

The full disposable load balancer reset should start as an operator command outside `go test`.
During early iteration, run it manually before a test attempt when the load balancer may contain
stale resources. The test itself should still register `t.Cleanup` for Kubernetes objects it
creates, and the same cleanup library can later be called from test cleanup once the behavior is
trusted.

## HTTP MVP Scenario

The first test should cover a single happy path plus route removal:

Before running the test during early development, use the manual cleanup command if the disposable
load balancer may not be empty. This happens outside `go test`.

The test body should:

1. Create a unique namespace, for example `oke-gw-e2e-<short-random>`.
2. Register cleanup for the namespace and route resources with `t.Cleanup`.
3. Create `GatewayClass`.
4. Create `GatewayConfig` with the supplied load balancer OCID.
5. Create a `Gateway` with one HTTP listener:

```yaml
listeners:
  - name: http
    port: 80
    protocol: HTTP
```

6. Create the echo backend `Deployment` and `Service`.
7. Wait for the backend deployment to be available and endpoint slices to contain ready endpoints.
8. Create an `HTTPRoute` attached to `sectionName: http` with a `/echo` `PathPrefix` match.
9. Wait for:
   - Gateway `Accepted=True`.
   - Gateway `Programmed=True`.
   - HTTPRoute `Accepted=True`.
   - HTTPRoute `ResolvedRefs=True`.
10. Probe `http://<public-ip>/echo`.
11. Assert:
   - HTTP status is `200`.
   - Response JSON is the echo server response.
   - `requestURL` contains `/echo`.
   - `requestMethod` is `GET`.
12. Delete the `HTTPRoute`.
13. Wait for the route to be fully deleted.
14. Probe `/echo` until it no longer returns the echo response.
15. Assert via OCI that the route's programmed policy rule is gone.
16. Delete the namespace.

The full disposable load balancer reset remains an out-of-band manual command for the first
implementation. Once the cleanup behavior is trusted, the test suite can call the same reset in
`t.Cleanup` after the test-created Kubernetes resources are deleted.

### Removal Assertion

Before deleting the `HTTPRoute`, read the route annotation
`oke-gateway-api.gemyago.github.io/http-route-programmed-lb-policy-rules` and split it into rule
names. After route deletion completes:

1. Poll `GetRoutingPolicy` for the HTTP listener policy, currently `http_policy`.
2. Assert none of the captured rule names are present.
3. Probe `/echo` until it no longer returns the echo JSON response from the backend. Treat any
   non-`200` status, non-JSON body, or JSON body that is not the expected echo response as removed.

## Later HTTP Cases

After the MVP is stable, add a table of supported match cases:

- Path prefix.
- Exact path.
- Exact header.
- Supported header regex prefix form.
- Supported header regex suffix form.

Route update should be a separate scenario:

1. Create route with `/old` and `/stay`.
2. Verify both work.
3. Patch route to `/new` and `/stay`.
4. Verify `/new` and `/stay` work.
5. Verify `/old` no longer returns the echo response.
6. Assert previous OCI routing policy rule names were removed.

## Logging Practices

The e2e project should copy the root repo's logging style instead of importing it:

- Use `log/slog`.
- Keep a small `e2e/internal/diag` package for error attributes and logger construction.
- Prefer structured fields for resource names, namespace, load balancer ID, and work request ID.

This mirrors the controller's diagnostics while preserving project isolation.

## Linting

Use the same pinned `golangci-lint` binary as the root repo. The e2e project should have a local
target that runs from the root-managed binary:

```sh
cd e2e
../bin/golangci-lint run ./...
```

The root `Makefile` can later add convenience targets:

```make
.PHONY: e2e-lint e2e-test-http
e2e-lint: bin/golangci-lint
	cd e2e && ../bin/golangci-lint run ./...

e2e-test-http:
	cd e2e && go test -count=1 -timeout=30m ./...
```

Do not wire `e2e-test-http` into the default `make test` target.

## Completion Before Live Infrastructure Exists

Until a disposable load balancer and Kubernetes test environment are provisioned, implementation
work can only run:

- root lint/test for changed root files,
- e2e lint,
- e2e unit tests for helper packages, if any,
- compile-only checks for e2e packages.

Live e2e execution should be reported as not run, with the missing infrastructure inputs listed.
