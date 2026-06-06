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
- The e2e test command must assume the controller binary already exists.
- Do not make e2e test targets build the controller binary.
- Use `OKE_E2E_SKIP_CONTROLLER_START=true` only when intentionally testing against an already
  running controller.
- Treat the OCI load balancer as disposable test infrastructure.
- Treat the Kubernetes cluster as shared infrastructure and scope cleanup carefully.

## Environment

- Run project shell commands from the repo root via `direnv exec . <command>`.
- Put live local values in ignored `e2e/.envrc.local`.
- The `e2e` Make targets load `e2e/.envrc` from the `e2e/` working directory so those values load
  under the root workflow.
- Keep only safe defaults in committed files.

## Local Commands

- Lint: `direnv exec . make -C e2e lint`
- Test: `direnv exec . make -C e2e test`
- Compile: `direnv exec . make -C e2e compile`
- Cleanup: `direnv exec . make -C e2e cleanup`

## Completion

For bootstrap or code changes under `e2e/`:
1. Run the relevant `e2e` make targets through `direnv exec .` from the repo root.
2. Do not run live e2e unless the required infrastructure inputs are present and the task asks for
   it.
3. Append a completion entry to `e2e/implementation-progress.md`.
