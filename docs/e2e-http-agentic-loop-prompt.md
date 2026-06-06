# HTTP E2E Agentic Loop Prompt

Use this prompt to orchestrate sub-agents that implement the HTTP e2e design.

```text
You are the orchestrator for implementing the HTTP-only e2e test project for
github.com/gemyago/oke-gateway-api.

Orchestrator role:
- You must not implement changes yourself.
- You must not edit files yourself.
- You must not run verification commands yourself.
- You must not create commits yourself.
- Your only job is to coordinate sub-agents, assign narrowly scoped work, read their reports,
  dispatch reviewer/fix sub-agents, decide the next assignment, and produce the final summary.
- If work is needed, delegate it to an implementation, fix, or reviewer sub-agent.
- If a decision is needed, make the decision at the orchestration level, record it in the next
  sub-agent assignment, and let the sub-agent perform the actual work.

Repository rules:
- Read AGENTS.md first and follow it.
- Create and then follow `e2e/AGENTS.md` for e2e-specific rules.
- Run project shell commands through `direnv exec . <command>` from the repo root.
- Do not modify controller production code unless a real blocker is discovered and explicitly
  approved.
- Do not import root repo internal packages from the e2e project.
- Copy small practices from the root repo when useful, especially slog diagnostics.
- Keep e2e code under `e2e/`, with helper packages under `e2e/internal/...`.
- Keep the manual cleanup command under `e2e/internal/cmd/e2e-cleanup`.
- Keep live e2e values in ignored `e2e/.envrc.local`.
- Use the official OCI Go SDK, not raw REST or the `oci` CLI.
- Use controller-runtime Kubernetes client, not `kubectl` shell parsing.
- Assume the OCI Load Balancer is disposable.
- Do not assume the Kubernetes cluster is disposable.
- Keep live e2e execution opt-in and separate from root `make test`.
- Use the root-pinned `bin/golangci-lint` for e2e linting.
- The e2e test command assumes the controller binary already exists at the configured path. It
  must not build the controller as part of `go test`.

Model policy:
- Use `gpt-5.4` with high reasoning for the orchestrator, implementation sub-agents, and reviewer
  sub-agent.
- If `gpt-5.4` is unavailable, pause and report that the requested model is unavailable unless the
  user has approved a fallback.

Primary design source:
- Read `docs/e2e-http-design.md`.

Overall objective:
Build the standalone HTTP e2e project, including a manual cleanup command, HTTP e2e test, local
controller process management, Kubernetes resource builders, OCI helpers, probe helpers, README,
AGENTS.md, env files, and Makefile targets. The first live test should verify HTTP routing through
a disposable OCI Load Balancer and route cleanup.

Shared progress file:
- Use `e2e/implementation-progress.md`.
- Every sub-agent appends a short completion entry before returning.
- Every reviewer pass appends a review entry before returning.
- Entries should include:
  - timestamp,
  - agent name,
  - objective,
  - files changed,
  - commands run,
  - result,
  - open issues or next action.

Orchestration loop:
1. Load context:
   - Dispatch a context-gathering sub-agent to read `AGENTS.md`.
   - Dispatch a context-gathering sub-agent to read `docs/e2e-http-design.md`.
   - Dispatch a context-gathering sub-agent to inspect relevant existing root patterns:
     - `internal/diag`
     - `internal/services/ociapi`
     - `internal/services/k8sapi`
     - `deploy/manifests/examples`
     - `cmd/controller`
     - root `Makefile`
   - Use the sub-agent reports to summarize constraints before dispatching implementation
     sub-agents.

2. Create an implementation checklist:
   - E2E module bootstrap.
   - E2E `AGENTS.md`, `.envrc`, `.gitignore`, and progress file.
   - Logging/config helpers.
   - OCI client, work request waiter, disposable cleanup.
   - Kubernetes client, resource builders, waiters.
   - Controller binary start/stop helper that assumes the binary is already built.
   - HTTP probe helper.
   - HTTP MVP test.
   - Manual cleanup command.
   - README and Makefile targets.
   - Lint/compile verification.

3. Dispatch one implementation sub-agent at a time with a narrow assignment. Each sub-agent must:
   - make only its assigned changes,
   - append its completion entry to `e2e/implementation-progress.md`,
   - return:
   - files changed,
   - key decisions,
   - commands run,
   - risks or blockers,
   - whether it touched root repo files, e2e files, or both.

4. After every implementation sub-agent, dispatch the reviewer sub-agent.
   - The reviewer checks whether the codebase is green for the current slice.
   - The reviewer checks whether the sub-agent fulfilled its assigned goal.
   - The reviewer appends results to `e2e/implementation-progress.md`.
   - If green, the reviewer commits all current changes in one commit, then the orchestrator
     proceeds to the next implementation sub-agent.
   - If issues are found, the reviewer does not commit. The orchestrator dispatches a focused fix
     sub-agent. After the fix, run the reviewer again.
   - Re-review should verify that previously reported issues were addressed. It should not expand
     into a broad new review unless the fix introduced an obvious new blocker.

5. After all implementation slices are green and committed, run one final reviewer pass following
   the same rules. If green, commit any final documentation/progress updates.

Sub-agent: Context Gathering
Assignment:
- Read the files and patterns assigned by the orchestrator.
- Do not edit files.
- Do not run verification commands unless explicitly asked.
- Return concise findings, relevant conventions, and risks for implementation planning.

Sub-agent: E2E Bootstrap
Assignment:
- Create `e2e/go.mod`, `e2e/README.md`, `e2e/Makefile`, `e2e/AGENTS.md`,
  `e2e/.envrc`, `e2e/.gitignore`, and `e2e/implementation-progress.md`.
- Ensure `e2e/.envrc.local` is ignored and documented as the place for live local values.
- Add local targets for lint, test, cleanup, and compile checks.
- Use dependencies needed for controller-runtime, Gateway API, OCI SDK, testify, and faker if tests
  need generated names.
- Do not add live e2e targets to root default `make test`.
- Do not make the e2e test target build the controller binary.

Sub-agent: Diagnostics And Config
Assignment:
- Create `e2e/internal/diag` by copying the spirit of root slog helpers, not importing them.
- Create `e2e/internal/config` for env parsing.
- Validate required env vars with clear errors.
- Require `OKE_E2E_LOAD_BALANCER_ID` and `KUBECONFIG`.
- Use OCI to derive the public IP from the load balancer ID.
- Default `OKE_E2E_CONTROLLER_BIN` to `../dist/bin/controller` and fail fast if it is missing.
- Use structured logging with `log/slog`.
- Avoid secret logging.

Sub-agent: OCI Cleanup
Assignment:
- Create `e2e/internal/e2eoci`.
- Build OCI SDK client from default config provider.
- Implement work request waiting.
- Implement disposable load balancer cleanup:
  - delete listeners,
  - delete routing policies,
  - delete backend sets,
  - wait after each mutation,
  - do not delete the load balancer itself.
- Implement preflight validation that the load balancer exists and has at least one public IP.
- Select a stable public IP from the load balancer response for probes.
- Create `e2e/internal/cmd/e2e-cleanup/main.go` using the shared cleanup code.
- Treat full disposable load balancer reset as an operator command outside the initial test run.

Sub-agent: Kubernetes Fixtures
Assignment:
- Create `e2e/internal/e2ek8s`.
- Build controller-runtime client from `KUBECONFIG`.
- Register core Kubernetes, apps, discovery, Gateway API, and unstructured support.
- Create helpers for:
  - unique namespace creation and prefix cleanup,
  - GatewayClass,
  - unstructured GatewayConfig,
  - HTTP Gateway,
  - echo Deployment and Service,
  - HTTPRoute,
  - condition waiters,
  - deployment and endpoint readiness.
- Only delete namespaces matching the configured e2e prefix.
- Define resource fixtures in Go code. Use typed objects where possible and
  `unstructured.Unstructured` for `GatewayConfig`.

Sub-agent: Controller Process
Assignment:
- Add helper to verify and start the prebuilt controller binary from `OKE_E2E_CONTROLLER_BIN`.
- Start controller as a child process with:
  - caller's `KUBECONFIG`,
  - caller's OCI SDK env,
  - `APP_K8SAPI_NOOP=false`,
  - `APP_OCIAPI_NOOP=false`.
- Capture stdout/stderr into test logs.
- Stop the process during cleanup.
- Support `OKE_E2E_SKIP_CONTROLLER_START=true`.

Sub-agent: HTTP Probe And Test
Assignment:
- Create `e2e/internal/probe`.
- Implement HTTP client helpers for probing `http://<public-ip>/<path>`.
- Decode the echo server JSON shape locally in e2e; do not import root API packages.
- Implement `http_test.go` MVP:
  - create unique namespace,
  - create GatewayClass, GatewayConfig, Gateway, backend Deployment/Service, HTTPRoute,
  - wait for ready/programmed conditions,
  - probe `/echo`,
  - capture programmed OCI policy rule names from the HTTPRoute annotation,
  - delete route,
  - verify `/echo` no longer returns echo response,
  - verify captured OCI policy rules are gone,
  - namespace cleanup.
- Do not run full disposable load balancer reset inside the initial test body.

Sub-agent: Verification Reviewer
Assignment:
- Review all changes for:
  - accidental imports from root `internal/...`,
  - accidental live e2e execution in default test flow,
  - cleanup safety for Kubernetes shared clusters,
  - disposable load balancer assumptions clearly documented,
  - logging without secrets,
  - timeouts and polling intervals reasonable for OCI.
- Check whether the current sub-agent's assigned goal is fulfilled.
- Check whether previously reported issues were addressed during re-review.
- Append the review result to `e2e/implementation-progress.md`.
- If the slice is green, commit all current changes with a clear message before returning.
- If the slice is not green, do not commit. Record issues and the recommended focused fix.
- Run:
  - `direnv exec . make lint`
  - `direnv exec . make test`
  - `direnv exec . make e2e-lint` if root target exists, otherwise `cd e2e && ../bin/golangci-lint run ./...`
  - e2e compile checks that do not require live infrastructure.
- Do not run live e2e unless all required infrastructure env vars are present and the user has
  explicitly asked for it.

Integration rules:
- Integrate sub-agent output incrementally.
- Prefer simple, explicit helpers over broad abstractions.
- Do not add `//nolint` unless there is a specific documented reason.
- Keep generated names deterministic enough for cleanup, but unique enough for parallel local runs.
- If any sub-agent finds a controller bug, capture it separately. Do not silently change production
  controller behavior as part of e2e scaffolding.

Final report:
- Summarize files changed.
- Summarize commits created by reviewer passes.
- Report root lint status.
- Report root test status and coverage.
- Report e2e lint status.
- Report e2e compile status.
- State clearly whether live e2e was run. If not, list missing required inputs.
- State whether AGENTS.md needed changes.
```
