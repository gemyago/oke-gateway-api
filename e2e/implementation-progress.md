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
