<!-- AGENTS.md — instructions for coding agents working under e2e/. Nearest file wins. -->

## Scope

- These rules apply to everything under `e2e/`.
- Keep this module separate from controller production code.
- Do not modify files outside `e2e/` unless the root `AGENTS.md` truly needs a matching workflow
  update.

## Boundaries

- Do not import root repo `internal/...` packages from the e2e module.
- Prefer public upstream packages and e2e-local helpers under `e2e/internal/...`.
- Keep root-level non-test Go code out of this module unless it is needed for the e2e module
  itself.

## Live Test Rules

- Live e2e stays opt-in and separate from the root `make test` flow.
- Treat `direnv exec . make -C e2e test` as a support-only check for e2e-owned helper packages.
- Use `direnv exec . make -C e2e run-e2e-tests` as the explicit live path.
- The live `run-e2e-tests` target builds the controller binary before running `go test .`.
- The selected cluster must already have the `GatewayConfig` CRD installed before live HTTP e2e
  runs; missing CRDs should fail the live test rather than being created by the helper.
- Do not make support-only targets such as `test` or `compile` build the controller binary.
- Use `OKE_E2E_SKIP_CONTROLLER_START=true` only when intentionally testing against an already
  running controller.
- Treat the OCI load balancer as disposable test infrastructure.
- Treat the Kubernetes cluster as shared infrastructure and scope cleanup carefully.
- Required live Kubernetes config is `OKE_E2E_KUBE_CONTEXT`; `KUBECONFIG` is optional.
- The e2e client and any child controller process must use the explicit
  `OKE_E2E_KUBE_CONTEXT`.

## Environment

- Run project shell commands from the repo root via `direnv exec . <command>`.
- User must prepare `e2e/.envrc.local` with actual values. **DO NOT** read this file.
- The `e2e` Make targets load `e2e/.envrc` from the `e2e/` working directory so those values load under the root workflow.
- Keep only safe defaults in committed files.

## Local Commands

By default assume you have to run any project specific command via `direnv exec <working-dir> <command>` to make sure env is loaded.

Assuming commands are run from the repo root:
- Lint: `make -C e2e lint`
- Test: `make -C e2e test` (support-only)
- Compile: `make -C e2e compile`
- Infra cleanup: `make -C e2e infra-cleanup`

## Running e2e tests

To run e2e tests, use `make -C e2e run-e2e-tests`. Do not run them automatically, only when user explicitly asks for it.

### Run individual e2e tests

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
