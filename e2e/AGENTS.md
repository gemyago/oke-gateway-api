<!-- AGENTS.md — instructions for coding agents working under e2e/. Nearest file wins. -->

## Scope

- These rules apply to everything under `e2e/`.
- Keep this module separate from controller production code.
- Do not modify files outside `e2e/` unless the root `AGENTS.md` truly needs a matching workflow
  update.

## Boundaries

- Do not import root repo `internal/...` packages from the e2e module.
- Prefer public upstream packages and e2e-local helpers under `e2e/internal/...`.
- Keep root-level non-test Go code out of this module unless it is needed for the e2e module itself.
- Never export any root level code in the e2e tests.

## Module Shape

- Keep shared live-test setup and cleanup helpers in `e2e/http_test.go`.
- Keep concrete live cases in separate top-level test files such as:
  - `e2e/http_startup_test.go`
  - `e2e/http_route_lifecycle_test.go`
- Keep reusable support code under `e2e/internal/...`:
  - `internal/config` for env parsing and validation
  - `internal/controllerproc` for child controller lifecycle
  - `internal/e2ek8s` for Kubernetes clients, fixtures, and waiters
  - `internal/e2eoci` for OCI inspection and cleanup helpers
  - `internal/probe` for HTTP probing
  - `internal/diag` for local slog helpers
- Normal test cleanup should remove only test-created Kubernetes resources.
- Broader OCI load balancer reset belongs in the explicit `make -C e2e infra-cleanup` operator
  path, not the default live test flow.

## Local Commands

**Shell usage notes**
- Follow root AGENTS.md for shell usage notes as base guidance.
- All commands mentioned here are assumed run from the `e2e/` working directory. If your shell is ephemeral, you would normally need to `direnv exec e2e <command>`.

Assuming commands are run from the repo root:
- Lint: `make lint`
- Test: `make test` (support-only)
- Preflight: `make preflight`
  - Uses the real Kubernetes and OCI Go clients in read-only mode
- Compile: `make compile`
- Infra cleanup: `make infra-cleanup`
  - Requires both `OKE_E2E_LOAD_BALANCER_ID` and `OKE_E2E_KUBE_CONTEXT`

## Live Test Rules

Live tests are tests that run against real infrastructure (e.g the actual e2e tests)

- Live e2e stays opt-in and separate from the root `make test` flow.
- Treat `make test` as a support-only check for e2e-owned helper packages.
- Use `make preflight` for read-only live environment checks.
- Use `make run-e2e-tests` as the explicit live path.
- The live `run-e2e-tests` target builds the controller binary, runs preflight, and then runs `go test .`.
- Treat the OCI load balancer as disposable test infrastructure.
- Treat the Kubernetes cluster as shared infrastructure and scope cleanup carefully.
- Required live Kubernetes config is `OKE_E2E_KUBE_CONTEXT`; `KUBECONFIG` is optional.
- The e2e client and any child controller process must use the explicit
  `OKE_E2E_KUBE_CONTEXT`.

## Logs

- E2E test logs written via the shared test logger go to `e2e/test.log`.
- Child controller stdout/stderr captured by `internal/controllerproc` go to
  `e2e/controller.log`.
- If you need to adjust test logging behavior, start with `e2e/testing.go` and
  `e2e/internal/diag/testing.go`.
- If you need to adjust child controller logging behavior, start with
  `e2e/internal/controllerproc/controller.go`.

Use `logTestProgress` or `logTestProgressContext` to log progress inside of the test. Use it for a high-level overview of the test progress, don't go verbose on very line.

## Run individual e2e tests

Build the controller binary first:

```sh
# Always force just to be safe.
make -B dist/bin
```

Then run the tests (from e2e directory):
```sh
go test -v . --run TestHTTP/Startup
go test -v . --run TestHTTP/RouteLifecycle
```

When iterating on a single test, if anything fails, you may consider cleanup: `make -C e2e infra-cleanup` and try again.

## Task Completion Protocol

### Coding Task Completion Protocol

Apply this when any Go, YAML, config, or other code-related files changed.

Always do all of the following before reporting completion:
1. Run `direnv exec . make lint` and confirm no errors.
2. Run `direnv exec . make compile` and confirm everything builds.
3. Run `direnv exec . make test` and confirm all tests pass.
4. Update this file if commands, workflows, or architecture changed.

Report completion status:
- Lint/Compile: ✓ no errors
- Tests: ✓ all passing, coverage XX.XX%
- AGENTS.md: ✓ updated / no changes needed

### Non-Coding Task Completion Protocol

Same as root.
