# E2E Implementation Progress

## 2026-06-07 Bootstrap

- Created the standalone `e2e` Go module scaffold and local contributor rules.
- Added local `lint`, `test`, `compile`, and `cleanup` targets that stay separate from the root
  default test flow.
- Documented `e2e/.envrc.local` as the ignored home for developer-specific live values.

### Completion Entry

- Validation run:
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e cleanup`
- Live e2e status: not run.
- Root repo files changed: none.

## 2026-06-07 Reviewer Entry

- Status: not green
- Verification run:
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . bash -lc 'cd e2e && ../bin/golangci-lint run ./...'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e test`
  - `direnv exec . bash -lc 'cd e2e && printf "PREFIX=%s\nCLASS=%s\nPORT=%s\nBIN=%s\nSKIP=%s\n" "$OKE_E2E_NAMESPACE_PREFIX" "$OKE_E2E_GATEWAY_CLASS_NAME" "$OKE_E2E_HTTP_PORT" "$OKE_E2E_CONTROLLER_BIN" "$OKE_E2E_SKIP_CONTROLLER_START"'`
- Finding:
  - The bootstrap documents `e2e/.envrc` and `e2e/.envrc.local` as the source of safe defaults and local live overrides, but those files are not loaded when following the required repo workflow `direnv exec . <command>` from the repo root. The verification probe above printed all `OKE_E2E_*` values as empty after `cd e2e`, so the live configuration path is currently not wired into the documented command flow.
- Recommended fix:
  - Make the e2e command entrypoint load the `e2e` direnv context, or move the documented defaults and override mechanism into the actual root-invoked workflow.

## 2026-06-07 Env Wiring Fix

- Status: green
- Decision:
  - Kept the fix inside `e2e/` by making the root-invoked `e2e` Make targets load `e2e/.envrc`
    before running each command, which preserves `direnv exec . <command>` from the repo root and
    keeps developer-specific live values in ignored `e2e/.envrc.local`.
- Files changed:
  - `e2e/Makefile`
  - `e2e/README.md`
  - `e2e/AGENTS.md`
  - `e2e/implementation-progress.md`
- Verification run:
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e test`
  - `direnv exec . bash <<'EOF' ... EOF`
- Verification result:
  - The explicit env probe loaded the documented defaults under the repo-root workflow:
    `OKE_E2E_NAMESPACE_PREFIX=oke-gw-e2e-`,
    `OKE_E2E_GATEWAY_CLASS_NAME=oke-gateway-api-e2e`,
    `OKE_E2E_HTTP_PORT=80`,
    `OKE_E2E_CONTROLLER_BIN=../dist/bin/controller`,
    `OKE_E2E_SKIP_CONTROLLER_START=false`.
- Live e2e status: not run.
- Root repo files changed: none.

## 2026-06-07 Re-review Verification

- Status: green
- Scope:
  - Re-checked only the bootstrap env-wiring slice and its immediate guardrails.
- Verification run:
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . bash -lc 'cd e2e && ../bin/golangci-lint run ./...'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e test`
  - `direnv exec . bash -lc 'make -C e2e -f - print-env <<\"EOF\" ... EOF'`
- Verification result:
  - The documented repo-root workflow still works, and the inline `print-env` target using
    `e2e/Makefile`'s `E2E_ENV` loaded the expected defaults:
    `OKE_E2E_NAMESPACE_PREFIX=oke-gw-e2e-`,
    `OKE_E2E_GATEWAY_CLASS_NAME=oke-gateway-api-e2e`,
    `OKE_E2E_HTTP_PORT=80`,
    `OKE_E2E_CONTROLLER_BIN=../dist/bin/controller`,
    `OKE_E2E_SKIP_CONTROLLER_START=false`.
  - No root repo `internal/...` imports were present in `e2e` Go files.
  - The default root `make test` flow still excluded live e2e; it exercised only the root module,
    while `e2e` remained opt-in through its separate Make targets.
- Live e2e status: not run.
- Root repo files changed: none.

## 2026-06-07 Diagnostics And Config

- Status: green
- Scope:
  - Added local `e2e/internal/diag` slog helpers and `e2e/internal/config` env parsing without
    importing root repo `internal/...` packages.
- Decisions:
  - Kept config explicit and OCI-oriented with separate Kubernetes, OCI, and controller sections so
    later slices can derive the load balancer public IP from `OKE_E2E_LOAD_BALANCER_ID` without
    reshaping the config contract.
  - Validated required envs and controller binary presence in one pass for clearer setup failures.
  - Kept config logging safe by exposing only non-secret structured attributes and presence flags.
- Files changed:
  - `e2e/internal/config/env.go`
  - `e2e/internal/config/env_test.go`
  - `e2e/internal/diag/attributes.go`
  - `e2e/internal/diag/slog.go`
  - `e2e/internal/diag/slog_test.go`
  - `e2e/README.md`
  - `e2e/implementation-progress.md`
- Verification run:
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
- Live e2e status: not run.
- Root repo files changed: none.

## 2026-06-07 Reviewer Verification - HTTP Probe And Test

- Reviewer: Codex verification sub-agent
- Status: green
- Findings:
  - No blocking issues found in the HTTP probe and test slice.
- Checks:
  - Confirmed the e2e module keeps imports inside `e2e/internal/...` and does not reach into root
    `internal/...`.
  - Confirmed the live HTTP path remains outside the root default test flow; `direnv exec . go list
    ./...` from the repo root still excludes the nested `e2e` module.
  - Confirmed the live HTTP test skips when `KUBECONFIG` or `OKE_E2E_LOAD_BALANCER_ID` is absent.
  - Confirmed shared-cluster cleanup stays scoped to the unique namespace plus the test-created
    `GatewayClass`.
  - Confirmed the live path uses e2e-local real helpers for Kubernetes, OCI, controller startup,
    and HTTP probing.
  - Confirmed the probe decodes the echo JSON response shape locally inside `e2e/internal/probe`.
  - Confirmed the test asserts route deletion, HTTP echo disappearance, and OCI routing policy rule
    disappearance after `HTTPRoute` deletion.
  - Confirmed the initial test body does not perform a full disposable load balancer reset.
  - Confirmed the slice stayed within the assigned HTTP probe/test scope.
- Verification run:
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . bash -lc 'cd e2e && ../bin/golangci-lint run ./...'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . bash -lc 'cd e2e && go test -count=1 ./internal/...'`
  - `direnv exec . go list ./...`
- Live e2e status: not run.

## 2026-06-07 Reviewer Verification - Diagnostics And Config

- Reviewer: Codex verification sub-agent
- Status: not green
- Findings:
  - `e2e/internal/config/env.go` validates `OKE_E2E_CONTROLLER_BIN` unconditionally, even when
    `OKE_E2E_SKIP_CONTROLLER_START=true`. That blocks the explicit "already running controller"
    mode described in `e2e/AGENTS.md`, because the config loader still fails if the local binary is
    absent even though this path should not start it.
- Verification run:
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . bash -lc 'cd e2e && ../bin/golangci-lint run ./...'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . go list ./...`
- Live e2e status: not run.
- Commit created: no
- Smallest focused fix:
  - Skip `validateControllerBin(...)` when `cfg.Controller.SkipStart` is true, and add a unit test
    covering the missing-binary + skip-start case.

## 2026-06-07 Fix - Diagnostics And Config Skip-Start Validation

- Status: green
- Scope:
  - Gated controller binary validation in `e2e/internal/config/env.go` on
    `!cfg.Controller.SkipStart`.
  - Added a regression test in `e2e/internal/config/env_test.go` covering
    missing controller binary with `OKE_E2E_SKIP_CONTROLLER_START=true`.
- Decisions:
  - Kept the fix inside the e2e config slice only, matching the reviewer-requested smallest change.
  - Preserved controller binary validation for normal startup mode and skipped it only for the
    explicit already-running-controller path.
- Files changed:
  - `e2e/internal/config/env.go`
  - `e2e/internal/config/env_test.go`
  - `e2e/implementation-progress.md`
- Verification run:
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make lint`
  - `direnv exec . make test`
- Live e2e status: not run.
- Root repo files changed: none.

## 2026-06-07 Re-Review Verification - Diagnostics And Config

- Reviewer: Codex verification sub-agent
- Status: green
- Findings:
  - Verified `OKE_E2E_CONTROLLER_BIN` is no longer required when
    `OKE_E2E_SKIP_CONTROLLER_START=true`; the focused regression test passed with a missing binary
    path.
  - Re-checked the `e2e` module boundary and found no imports of root repo `internal/...`
    packages.
  - Re-confirmed the root default `make test` flow still excludes live e2e; `direnv exec . go list
    ./...` listed only root-module packages, while `e2e` remained behind its separate Make targets.
- Verification run:
  - `direnv exec . bash -lc 'cd e2e && go test ./internal/config -run "TestLoadFromEnv/skips_controller_binary_validation_when_skip_start_is_enabled" -count=1'`
  - `direnv exec . go list ./...`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . bash -lc 'cd e2e && ../bin/golangci-lint run ./...'`
- Live e2e status: not run.
- Commit created: yes, `e2e: add diagnostics and config bootstrap`

## 2026-06-07 OCI Cleanup

- Status: green
- Scope:
  - Added `e2e/internal/e2eoci` for OCI load balancer client construction, work request waiting,
    preflight validation, stable public IP selection, and disposable load balancer child cleanup.
  - Finished `e2e/internal/cmd/e2e-cleanup/main.go` as an OCI-only operator command using the shared
    cleanup code.
- Decisions:
  - Kept the cleanup surface OCI-specific and explicit: the command reads only OCI env inputs and
    `OKE_E2E_LOAD_BALANCER_ID`, so it does not pull Kubernetes requirements into this slice.
  - Reset order is listener -> routing policy -> backend set, with an OCI work request wait after
    every successful delete, and the load balancer itself is left intact.
  - Stable probe targeting uses the lexicographically smallest public IP from the load balancer
    response to avoid response-order drift.
- Files changed:
  - `e2e/internal/e2eoci/client.go`
  - `e2e/internal/e2eoci/workrequests.go`
  - `e2e/internal/e2eoci/cleanup.go`
  - `e2e/internal/e2eoci/e2eoci_test.go`
  - `e2e/internal/cmd/e2e-cleanup/main.go`
  - `e2e/README.md`
  - `e2e/go.mod`
  - `e2e/go.sum`
  - `e2e/implementation-progress.md`
- Verification run:
  - `direnv exec . bash -lc 'cd e2e && go mod download'`
  - `direnv exec . bash -lc 'cd e2e && go mod tidy'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
- Live e2e status: not run.
- Root repo files changed: none.

## 2026-06-07 Reviewer Verification - OCI Cleanup

- Reviewer: Codex verification sub-agent
- Status: green
- Findings:
  - No blocking issues found in the OCI cleanup slice.
  - Verified the `e2e` module still does not import root repo `internal/...` packages.
  - Re-confirmed the root default `make test` flow still excludes live e2e because the root module
    and `e2e` module are listed separately.
  - Confirmed the cleanup path stays OCI-only, deletes listeners before routing policies before
    backend sets, waits for the OCI work request after each successful mutation, never deletes the
    load balancer itself, and keeps logs limited to non-secret operational fields.
  - Confirmed preflight inspection requires a load balancer id, resolves the load balancer, and

## 2026-06-08 Reviewer Verification - Slice 1 E2E Refactor

- Timestamp: 2026-06-08 00:50:57 CEST
- Reviewer: Codex reviewer sub-agent
- Objective:
  - Verify only slice 1 of the OKE Gateway API e2e refactor:
    - `make -C e2e test` stays support-only under `./internal/...`
    - `make -C e2e run-e2e-tests` exists and builds the controller before package-level live tests
    - `OKE_E2E_KUBE_CONTEXT` is required while `KUBECONFIG` remains optional
    - the e2e Kubernetes client forces the requested context
    - focused tests for the above are present and passing
- Files changed:
  - `e2e/implementation-progress.md`
- Commands run:
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e -n run-e2e-tests`
  - `direnv exec . bash -lc 'cd e2e && go test -count=1 ./internal/config -run "TestLoadFromEnv/(uses_defaults_and_preferred_oci_env_names|returns_clear_validation_errors)"'`
  - `direnv exec . bash -lc 'cd e2e && go test -count=1 ./internal/e2ek8s -run "TestBuildRESTConfig|TestNewClient"'`
- Result:
  - Status: green
  - Verified `e2e/Makefile` keeps `test` scoped to `./internal/...` and keeps live package tests
    separate in `run-e2e-tests`.
  - Verified `run-e2e-tests` builds the controller first via `make -C .. dist/bin` and then runs
    `go test -count=1 .`.
  - Verified `e2e/internal/config/env.go` requires `OKE_E2E_KUBE_CONTEXT` and treats
    `KUBECONFIG` as optional.
  - Verified `e2e/internal/e2ek8s/client.go` forces the configured kube context through
    `clientcmd.ConfigOverrides{CurrentContext: ...}`.
  - Verified focused coverage exists in `e2e/internal/config/env_test.go` and
    `e2e/internal/e2ek8s/e2ek8s_test.go`, and the targeted tests passed.
- Open issues / next action:
  - None for this slice.
    selects a stable public IP by sorting the discovered public addresses.
- Verification run:
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . bash -lc 'cd e2e && ../bin/golangci-lint run ./...'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e test`
  - `direnv exec . go list ./...`
  - `direnv exec . bash -lc 'cd e2e && go list ./...'`
- Live e2e status: not run.
- Commit created: yes, `e2e: add OCI cleanup workflow`

## 2026-06-07 Kubernetes Fixtures

- Status: green
- Scope:
  - Added `e2e/internal/e2ek8s` with controller-runtime client construction from `KUBECONFIG`,
    typed Kubernetes and Gateway API fixtures, unstructured `GatewayConfig` creation, namespace
    helpers, and readiness/condition waiters.
  - Documented the new fixture layer and the namespace-prefix cleanup guard in `e2e/README.md`.
- Decisions:
  - Kept the e2e module independent from root repo `internal/...` packages by defining
    `GatewayConfig` as `unstructured.Unstructured` with the public CRD group/version/kind.
  - Registered Kubernetes core, apps, discovery, and Gateway API types in a dedicated e2e scheme,
    and wrapped the controller-runtime client in an e2e-local struct so constructors still return a
    concrete type.
  - Restricted namespace cleanup to names beginning with the configured e2e prefix, which keeps the
    helper safe for shared clusters.
  - Kept the echo backend fixture aligned with the example deployment image and HTTP wiring already
    documented in the repo, while leaving controller process management and live HTTP probing for
    later slices.
- Files changed:
  - `e2e/internal/e2ek8s/client.go`
  - `e2e/internal/e2ek8s/e2ek8s_test.go`
  - `e2e/internal/e2ek8s/fixtures.go`
  - `e2e/internal/e2ek8s/namespace.go`
  - `e2e/internal/e2ek8s/wait.go`
  - `e2e/README.md`
  - `e2e/go.mod`
  - `e2e/go.sum`
  - `e2e/implementation-progress.md`
- Verification run:
  - `direnv exec . bash -lc 'cd e2e && go mod download'`
  - `direnv exec . bash -lc 'cd e2e && go mod download sigs.k8s.io/gateway-api github.com/jaswdr/faker/v2'`
  - `direnv exec . bash -lc 'cd e2e && go mod tidy'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
- Live e2e status: not run.
- Root repo files changed: none.

### Reviewer Verification - 2026-06-07

- Reviewer result: green
- Findings:
  - No root repo `internal/...` imports were added under `e2e/`; the new package only uses e2e-local
    config plus public Kubernetes, controller-runtime, and Gateway API packages.
  - The root `make test` flow remains separate from live e2e because the `e2e` module is outside the
    root module and `e2e/http_test.go` still skips the unimplemented live HTTP path.
  - Namespace cleanup remains prefix-guarded in `DeleteNamespacesWithPrefix`, and the README now
    documents that only namespaces starting with `OKE_E2E_NAMESPACE_PREFIX` are eligible.
  - Fixtures stay typed for core Kubernetes and Gateway API resources, while `GatewayConfig`
    remains the only unstructured fixture.
  - The controller-runtime client is built from `KUBECONFIG`, and the wait helpers are compile- and
    unit-validated without starting live infrastructure.
  - The slice stayed scoped to fixture/client/waiter groundwork and documentation; it did not spill
    into controller orchestration or live e2e execution.
- Verification run:
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . bash -lc 'cd e2e && ../bin/golangci-lint run ./...'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e test`
  - `direnv exec . bash -lc 'cd e2e && go list ./...'`
  - `direnv exec . bash -lc 'cd e2e && go test -run '^$' ./...'`
  - `direnv exec . bash -lc 'cd e2e && go test ./internal/e2ek8s -run TestWaiters -count=1 -v'`
- Live e2e status: not run.

## 2026-06-07 Controller Process

- Status: green
- Scope:
  - Added `e2e/internal/controllerproc` to verify and launch the prebuilt controller binary from
    `OKE_E2E_CONTROLLER_BIN` as a child process for live e2e tests.
  - Documented the controller helper startup, env forwarding, log capture, cleanup shutdown, and
    `OKE_E2E_SKIP_CONTROLLER_START=true` behavior in `e2e/README.md`.
- Decisions:
  - Kept controller orchestration test-oriented and e2e-local by exposing a small helper that
    accepts a test log sink, so stdout/stderr stream directly into test logs without importing root
    repo `internal/...` packages.
  - Forwarded the caller environment, then normalized `KUBECONFIG`, OCI config/profile env vars,
    and forced `APP_K8SAPI_NOOP=false` plus `APP_OCIAPI_NOOP=false` in the child process to match
    live-controller expectations.
  - Used offline unit tests with a temporary shell stub as the child process so compile, lint, and
    test verification stay infrastructure-free while still exercising process launch and shutdown.
- Files changed:
  - `e2e/internal/controllerproc/controller.go`
  - `e2e/internal/controllerproc/controller_test.go`
  - `e2e/README.md`
  - `e2e/implementation-progress.md`
- Verification run:
  - `direnv exec . bash -lc 'cd e2e && gofmt -w internal/controllerproc/controller.go internal/controllerproc/controller_test.go'`
  - `direnv exec . bash -lc 'cd e2e && go test ./internal/controllerproc -run TestStart -count=1'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
- Live e2e status: not run.
- Root repo files changed: none.

## 2026-06-07 Controller Process Review

- Reviewer: verification reviewer
- Status: not green
- Findings:
  - `e2e/internal/controllerproc/controller.go`: the forced-stop fallback treats a successfully
    killed controller as a cleanup failure. After `Process.Kill()` succeeds, `cmd.Wait()` commonly
    returns an `*exec.ExitError` for signal termination; `killAfterTimeout()` currently converts
    that expected result into `wait for killed controller process ...` and fails test cleanup.
    This makes the stop path flaky for any controller process that ignores or outlives SIGTERM.
- Verification run:
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . bash -lc 'cd e2e && ../bin/golangci-lint run ./...'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . bash -lc 'cd e2e && go test -run '^$' ./...'`
  - `direnv exec . bash -lc 'cd e2e && go test ./internal/controllerproc -count=1 -v'`
  - `direnv exec . bash -lc 'cd e2e && go list ./...'`
- Live e2e status: not run.

## 2026-06-07 Fix - Controller Process Forced Stop Cleanup

- Status: green
- Scope:
  - Kept the fix inside `e2e/internal/controllerproc` and the progress log.
- Decisions:
  - Treated a signaled `*exec.ExitError` as an expected result only after a successful forced kill,
    which fixes the cleanup false positive without changing the normal SIGTERM wait path.
  - Added a focused regression test with a TERM-ignoring temporary controller stub and a readiness
    line so the test deterministically exercises the timeout-to-kill branch.
- Files changed:
  - `e2e/internal/controllerproc/controller.go`
  - `e2e/internal/controllerproc/controller_test.go`
  - `e2e/implementation-progress.md`
- Verification run:
  - `direnv exec . bash -lc 'cd e2e && go test -run "^$" ./internal/controllerproc'`
  - `direnv exec . bash -lc 'cd e2e && ../bin/golangci-lint run ./internal/controllerproc/...'`
  - `direnv exec . bash -lc 'cd e2e && go test ./internal/controllerproc -run TestStart -count=1'`
- Live e2e status: not run.

## 2026-06-07 Re-review - Controller Process Verification

- Reviewer: verification reviewer
- Status: green
- Findings:
  - No blocking issues found in the controller process slice.
  - Verified the forced-stop cleanup path no longer reports a cleanup error after a successful
    kill; `TestStart/forced_stop_accepts_the_expected_signaled_wait_result_after_kill` passes and
    cleanup stays error-free.
  - No root repo `internal/...` imports are present under `e2e/`.
  - The root default `make test` flow still excludes live e2e; `direnv exec . go list ./...`
    listed only the root module packages, while `e2e` remained behind separate `e2e` Make targets.
- Verification run:
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . bash -lc 'cd e2e && ../bin/golangci-lint run ./...'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . bash -lc 'cd e2e && go test -run '^$' ./...'`
  - `direnv exec . bash -lc 'cd e2e && go test ./internal/controllerproc -count=1 -v'`
  - `direnv exec . go list ./...`
- Live e2e status: not run.

## 2026-06-07 HTTP Probe And Test

- Status: green
- Scope:
  - Added `e2e/internal/probe` with HTTP polling helpers that target the load balancer public IP
    and decode the echo JSON response shape locally inside the e2e module.
  - Replaced the `e2e/http_test.go` bootstrap skip with a live HTTP MVP that creates Gateway API
    resources, starts the controller unless skip-start is enabled, probes `/echo`, deletes the
    route, and verifies both HTTP removal and OCI routing policy rule cleanup.
  - Added small supporting waiters for HTTPRoute deletion and listener routing policy rule removal.
- Decisions:
  - Kept live execution opt-in by skipping the HTTP test unless `KUBECONFIG` and
    `OKE_E2E_LOAD_BALANCER_ID` are present, while still wiring the real Kubernetes, OCI, controller
    process, and HTTP probe helpers together for actual runs.
  - Captured programmed OCI rule names from the real `HTTPRoute` annotation contract instead of
    importing root repo internals into the e2e module.
  - Scoped cleanup to the unique namespace plus the test-created `GatewayClass`, and left the full
    disposable load balancer reset out of the main test body.
- Files changed:
  - `e2e/http_test.go`
  - `e2e/internal/probe/http.go`
  - `e2e/internal/probe/http_test.go`
  - `e2e/internal/e2ek8s/wait.go`
  - `e2e/internal/e2ek8s/e2ek8s_test.go`
  - `e2e/internal/e2eoci/cleanup.go`
  - `e2e/internal/e2eoci/routingpolicy.go`
  - `e2e/internal/e2eoci/e2eoci_test.go`
  - `e2e/README.md`
  - `e2e/implementation-progress.md`
- Verification run:
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
- Live e2e status: not run.
- Root repo files changed: none.

## 2026-06-07 Final Review - Full E2E Scaffold

- Reviewer: final verification reviewer
- Status: green
- Findings:
  - No blocking issues found across the assembled offline e2e scaffold.
  - No root repo `internal/...` imports are present under `e2e/`; the module stays isolated behind
    `e2e/internal/...` plus public upstream packages.
  - The root default `make test` flow still excludes live e2e because the nested `e2e` module does
    not appear in the root `go list ./...`, while `direnv exec . make -C e2e test` remains the
    explicit opt-in path.
  - Live test cleanup remains shared-cluster-safe on the Kubernetes side because the test deletes
    only the exact generated namespace and exact generated `GatewayClass`, while the OCI cleanup
    command remains limited to listeners, routing policies, and backend sets on the disposable load
    balancer and never deletes the load balancer itself.
  - Config and cleanup logging remain free of raw kubeconfig and OCI credential values; the logged
    fields stay limited to non-secret presence flags and operational identifiers.
  - Timeouts and polling intervals remain reasonable and explicit for offline review: 20 minute
    top-level live test and OCI work request timeouts, 10 second probe request timeout, and 2
    second polling intervals for Kubernetes, OCI routing policy, work request, and HTTP probe
    waiters.
  - Controller startup behavior, skip-start semantics, and missing-infra skip behavior are wired as
    documented: the controller helper can skip local startup when
    `OKE_E2E_SKIP_CONTROLLER_START=true`, and `TestHTTP` cleanly skips when `KUBECONFIG` or
    `OKE_E2E_LOAD_BALANCER_ID` is absent.
  - `e2e/README.md`, `e2e/AGENTS.md`, and `e2e/Makefile` remain aligned with the implemented
    repo-root `direnv exec .` workflow, and the lack of a root `e2e-lint` target matches the
    documented fallback lint invocation.
- Verification run:
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . bash -lc 'cd e2e && ../bin/golangci-lint run ./...'`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e test`
  - `direnv exec . go list ./...`
  - `direnv exec . bash -lc 'cd e2e && go list ./...'`
  - `direnv exec . bash -lc 'cd e2e && go test -count=1 ./internal/...'`
  - `direnv exec . bash -lc 'cd e2e && unset KUBECONFIG OKE_E2E_LOAD_BALANCER_ID && go test -count=1 -run "^TestHTTP$" ./...'`
- Live e2e status: not run.

## 2026-06-07 Fix - Default Kubeconfig Fallback

- Status: green
- Scope:
  - Made `KUBECONFIG` optional in the e2e env loader and let Kubernetes client/controller startup
    fall back to the default kubeconfig loading rules when it is unset.
  - Updated the live HTTP gate so missing `OKE_E2E_CONTROLLER_BIN` still skips the opt-in test with
    a clear build hint during offline verification.
  - Updated e2e docs and focused tests to match the new kubeconfig behavior.
- Files changed:
  - `e2e/internal/config/env.go`
  - `e2e/internal/config/env_test.go`
  - `e2e/internal/e2ek8s/client.go`
  - `e2e/internal/e2ek8s/e2ek8s_test.go`
  - `e2e/internal/controllerproc/controller.go`
  - `e2e/internal/controllerproc/controller_test.go`
  - `e2e/http_test.go`
  - `e2e/README.md`
  - `e2e/implementation-progress.md`
- Verification run:
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
- Live e2e status: not run.
- Root repo files changed: none.

## 2026-06-08 00:48:06 CEST - Implementation Worker 1

- Agent: implementation worker 1
- Objective:
  - Refactor the e2e runner/config plumbing for the current design inside the owned Makefile,
    config, and Kubernetes client files.
- Files changed:
  - `e2e/Makefile`
  - `e2e/internal/config/env.go`
  - `e2e/internal/config/env_test.go`
  - `e2e/internal/e2ek8s/client.go`
  - `e2e/internal/e2ek8s/e2ek8s_test.go`
  - `e2e/implementation-progress.md`
- Commands run:
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make -C e2e compile`
- Result:
  - `make -C e2e test` now runs only support-package tests via `go test -count=1 ./internal/...`.
  - Added `make -C e2e run-e2e-tests` to build the controller through the root `dist/bin` target
    and then run the package-level live tests in `.`.
  - `OKE_E2E_KUBE_CONTEXT` is now required config, `KUBECONFIG` remains optional, config logging
    includes the selected Kubernetes context, and the Kubernetes client now forces that context via
    `clientcmd.ConfigOverrides{CurrentContext: ...}` with focused coverage.
  - Verification status: green. Live e2e not run.
- Open issues / next action:
  - The child controller process still needs the same explicit kube-context enforcement in its own
    slice; this worker did not touch `controllerproc` per scope.

## 2026-06-08 01:02:31 CEST - Implementation Worker 2

- Agent: implementation worker 2
- Objective:
  - Refactor the controller-process and root HTTP live-test slice so the child controller honors
    `OKE_E2E_KUBE_CONTEXT`, exposes log-waiting for startup assertions, and splits the live HTTP
    coverage into focused startup and route-lifecycle cases.
- Files changed:
  - `e2e/internal/controllerproc/controller.go`
  - `e2e/internal/controllerproc/controller_test.go`
  - `e2e/http_test.go`
  - `e2e/http_startup_test.go`
  - `e2e/http_route_lifecycle_test.go`
  - `e2e/implementation-progress.md`
- Commands run:
  - `direnv exec . bash -lc 'cd e2e && go test -count=1 ./internal/controllerproc'`
  - `direnv exec . bash -lc 'cd e2e && go test -count=1 -short .'`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make -C e2e compile`
- Result:
  - The controller child process now gets a generated kubeconfig pinned to the requested context,
    records forwarded stdout/stderr lines, and lets tests wait for log fragments such as
    `Starting controller manager`.
  - The root live HTTP test is split into `Startup` and `RouteLifecycle` cases, with the startup
    case waiting for the controller-manager log line before stopping the process and the route
    lifecycle preserving the existing create/probe/delete assertions.
  - Live HTTP config loading now fails immediately on missing required inputs or a missing
    controller binary instead of skipping in the live path.
  - Verification status: green. Live e2e not run.
- Open issues / next action:
  - No known code issues in this slice. The remaining next step is a real `run-e2e-tests`
    execution against live infrastructure when that environment is ready.

## 2026-06-08 01:07:04 CEST - Reviewer Sub-Agent (Slice 2)

- Agent: reviewer sub-agent
- Objective:
  - Verify the slice-2 e2e refactor: controller/context alignment, startup log waiting, split live
    HTTP cases, fail-fast live config loading, preserved route lifecycle behavior, and no live e2e
    execution.
- Files changed:
  - `e2e/implementation-progress.md`
- Commands run:
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . bash -lc 'cd e2e && go test -race ./internal/controllerproc -run "Test(ProcessWaitForLog|Start)" -count=10'`
  - `direnv exec . bash -lc 'cd e2e && go test -count=1 -run "^TestHTTP$" ./...'`
  - `direnv exec . bash -lc 'cd e2e && go test ./internal/e2ek8s -run "TestBuildRESTConfig" -count=1 -v'`
- Result:
  - `make -C e2e lint`, `make -C e2e test`, and `make -C e2e compile` passed.
  - Verified the e2e client still forces `OKE_E2E_KUBE_CONTEXT` through
    `clientcmd.ConfigOverrides{CurrentContext: ...}`, and `controllerproc` now shapes a temporary
    kubeconfig for the same explicit context before launching the child controller.
  - Verified the root live HTTP suite is split into `Startup` and `RouteLifecycle`, and that a
    direct root-package `go test` now fails immediately on missing live config instead of skipping.
  - Verified no live e2e execution occurred: the normal `make -C e2e test` target still runs only
    `./internal/...`, while the focused root-package check failed during config validation before
    any cluster or OCI actions.
  - Verification status: not green.
- Open issues / next action:
  - `e2e/internal/controllerproc/controller.go` has a real data race in the new `WaitForLog`
    implementation: `appendLogLine` writes `p.logWait` under `logMu`, while `WaitForLog` reads
    `p.logWait` without synchronization. The focused race run reproduced this repeatedly.
  - Smallest focused fix: make `WaitForLog` read the wait channel under the same lock used by
    `appendLogLine` and check the buffered log lines in the same critical section, or replace the
    channel-swapping scheme with a `sync.Cond`/single-notification channel design that cannot miss
    a wake-up.

## 2026-06-08 01:10:23 CEST - Implementation Worker 2 Follow-Up

- Agent: implementation worker 2
- Objective:
  - Fix the reviewer-reported `WaitForLog` race and missed-wake-up window inside
    `e2e/internal/controllerproc`.
- Files changed:
  - `e2e/internal/controllerproc/controller.go`
  - `e2e/internal/controllerproc/controller_test.go`
  - `e2e/implementation-progress.md`
- Commands run:
  - `direnv exec . bash -lc 'cd e2e && go test -race ./internal/controllerproc -run "Test(ProcessWaitForLog|Start)" -count=10'`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make -C e2e compile`
- Result:
  - `WaitForLog` now snapshots the buffered log state and the current wait channel while holding
    `logMu`, which removes the unsynchronized channel read and closes the missed-notification gap
    between checking the log buffer and waiting for the next append.
  - Added a focused concurrent multi-waiter test that repeatedly races `WaitForLog` callers
    against `appendLogLine` and passed the repeated `-race` run.
  - Verification status: green. Live e2e not run.
- Open issues / next action:
  - No known remaining issue in this focused slice.

## 2026-06-08 01:11:42 CEST - Reviewer Re-Review (Slice 2 WaitForLog Fix)

- Agent: reviewer sub-agent
- Objective:
  - Re-review only the focused `WaitForLog` race and missed-wakeup fix in
    `e2e/internal/controllerproc`.
- Files changed:
  - `e2e/implementation-progress.md`
- Commands run:
  - `direnv exec . bash -lc 'cd e2e && go test -race ./internal/controllerproc -run "Test(ProcessWaitForLog|Start)" -count=10'`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make -C e2e compile`
- Result:
  - Verified `WaitForLog` now snapshots both the current log buffer state and the active wait
    channel while holding `logMu`, eliminating the earlier unsynchronized `p.logWait` read and the
    check-then-wait missed-notification gap.
  - Verified the focused concurrent regression test in
    `e2e/internal/controllerproc/controller_test.go` repeatedly exercises multi-waiter wake-ups.
  - All requested verification commands passed, including the repeated `-race` run.
  - Verification status: green. Live e2e not run.
- Open issues / next action:
  - None for this focused fix.

## 2026-06-08 01:18:56 CEST - Final Reviewer Pass (Assembled E2E Refactor)

- Agent: reviewer sub-agent
- Objective:
  - Final non-live review of the assembled e2e refactor across workflow wiring, config handling,
    explicit kube-context enforcement, split live HTTP cases, and e2e docs.
- Files changed:
  - `e2e/implementation-progress.md`
- Commands run:
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . bash -lc 'cd e2e && go test -race ./internal/controllerproc -run "Test(ProcessWaitForLog|Start)" -count=10'`
  - `direnv exec . make -C e2e -n run-e2e-tests`
  - `direnv exec . bash -lc 'cd e2e && go test -count=1 -run "^TestHTTP$" ./...'`
  - `direnv exec . bash -lc 'cd e2e && go test -race ./internal/controllerproc -run "Test(ProcessWaitForLog|Start)" -count=10'`
- Result:
  - Root `make lint` and `make test` passed.
  - `make -C e2e lint`, `make -C e2e test`, and `make -C e2e compile` passed.
  - Verified `make -C e2e test` remains support-only via `go test -count=1 ./internal/...`.
  - Verified `make -C e2e run-e2e-tests` exists and dry-runs as:
    `make -C .. dist/bin` followed by `go test -count=1 .`.
  - Verified `OKE_E2E_KUBE_CONTEXT` is required while `KUBECONFIG` remains optional in
    `e2e/internal/config/env.go`.
  - Verified the e2e client forces `clientcmd.ConfigOverrides{CurrentContext: ...}` and the child
    controller shapes a kubeconfig pinned to the same explicit context.
  - Verified the root live HTTP suite is split into `Startup` and `RouteLifecycle`, and a focused
    root-package `go test` fails immediately on missing live config instead of skipping.
  - Verified `e2e/README.md` and `e2e/AGENTS.md` document the support-only `test` target, the live
    `run-e2e-tests` target, required `OKE_E2E_KUBE_CONTEXT`, optional `KUBECONFIG`, and the
    Kubernetes manual pre-check commands.
  - No live e2e execution was performed. The live target was only dry-run, and the focused
    root-package check failed during config validation before any cluster or OCI actions.
  - Verification status: not green.
- Open issues / next action:
  - The assembled refactor has one remaining blocker in verification stability: the first run of
    `go test -race ./internal/controllerproc -run "Test(ProcessWaitForLog|Start)" -count=10`
    failed with `require.Eventually(...)` timeouts in
    `TestStart/shapes_kubeconfig_for_the_requested_Kubernetes_context`,
    `TestStart/allows_empty_kubeconfig_so_the_controller_can_use_default_loading`, and
    `TestStart/forced_stop_accepts_the_expected_signaled_wait_result_after_kill`, while the second
    run of the exact same command passed. That makes the reviewer-grade race verification flaky.
  - Smallest focused fix: de-flake the `controllerproc` package tests under repeated `-race`
    execution by reducing contention from `t.Parallel()` in the heavy subprocess cases and/or by
    replacing the polling-style `require.Eventually(...)` waits with direct synchronization on the
    relevant process/log events.

## 2026-06-08 01:15:44 CEST - Implementation Worker 3 Docs Alignment

- Agent: implementation worker 3
- Objective:
  - Update the e2e docs and scoped rules to match the implemented live-test refactor without
    changing code.
- Files changed:
  - `e2e/README.md`
  - `e2e/AGENTS.md`
  - `e2e/implementation-progress.md`
- Commands run:
  - `direnv exec . make -C e2e test`
  - `direnv exec . make -C e2e compile`
  - `direnv exec . make -C e2e -n run-e2e-tests`
- Result:
  - Documented `make -C e2e test` as support-only and `make -C e2e run-e2e-tests` as the explicit
    opt-in live path.
  - Documented that `run-e2e-tests` builds the controller first, while support-only targets do not.
  - Documented that `OKE_E2E_KUBE_CONTEXT` is required, `KUBECONFIG` is optional, and both the
    e2e client and child controller use the explicit context.
  - Added Kubernetes manual connectivity pre-checks that align with the explicit-context workflow.
  - Verification status: green for the requested support-only checks and live-target dry run. Live
    e2e not run.
- Open issues / next action:
  - The live cluster and OCI path was not exercised in this slice, so the new wording is aligned to
    the current implementation and dry-run output rather than a fresh live run.

## 2026-06-08 01:21:28 CEST - Implementation Worker 2 Final Controllerproc Test De-flake

- Agent: implementation worker 2
- Objective:
  - De-flake the repeated `controllerproc` race verification with the smallest focused test-side
    change.
- Files changed:
  - `e2e/internal/controllerproc/controller_test.go`
  - `e2e/implementation-progress.md`
- Commands run:
  - `direnv exec . bash -lc 'cd e2e && go test -race ./internal/controllerproc -run "Test(ProcessWaitForLog|Start)" -count=10'`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make -C e2e compile`
- Result:
  - Replaced the subprocess-heavy `require.Eventually(...)` polling in `TestStart` with direct
    `proc.WaitForLog(...)` synchronization.
  - Removed `t.Parallel()` from the three flaky subprocess-heavy subtests that were timing out
    under repeated `-race` execution while keeping the rest of the package structure intact.
  - Verification status: green, including the repeated `-race` run. Live e2e not run.
- Open issues / next action:
  - No known remaining issue in this focused slice.

## 2026-06-08 01:22:58 CEST - Reviewer Re-Review (Controllerproc Test Stability)

- Agent: reviewer sub-agent
- Objective:
  - Re-review only the focused `controllerproc` test-stability fix so the repeated `-race`
    verification can be trusted for the assembled e2e refactor.
- Files changed:
  - `e2e/implementation-progress.md`
- Commands run:
  - `direnv exec . bash -lc 'cd e2e && go test -race ./internal/controllerproc -run "Test(ProcessWaitForLog|Start)" -count=10'`
  - `direnv exec . make -C e2e lint`
  - `direnv exec . make -C e2e test`
  - `direnv exec . make -C e2e compile`
- Result:
  - Verified the flaky subprocess-heavy `TestStart` cases now wait on direct
    `proc.WaitForLog(...)` synchronization via the local helper instead of polling with
    `require.Eventually(...)`.
  - Verified the repeated `-race` controllerproc command passed cleanly.
  - Verified the requested `e2e` lint, support-only test, and compile checks also passed.
  - Verification status: green. Live e2e not run.
- Open issues / next action:
  - Residual risk is low and limited to ordinary subprocess timing variance, but the previously
    failing reviewer-grade repeated `-race` command is now passing in this worktree.
