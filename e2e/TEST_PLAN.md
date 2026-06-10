# E2E Test Plan

This document tracks the recommended live end-to-end test scenarios for the OKE Gateway API
controller.

## Goal

The purpose of the live e2e suite is not to duplicate unit coverage for route-to-OCI condition
mapping. Unit tests already cover a large part of the translation logic.

The live e2e suite should focus on behavior that unit tests cannot verify reliably:

- real Kubernetes resource reconciliation,
- real OCI load balancer programming,
- real HTTP routing behavior as interpreted by OCI,
- watch-driven updates from related resources such as EndpointSlices and TLS Secrets,
- cleanup behavior for programmed OCI listener policy rules.

## Selection Principles

Prefer live e2e coverage when at least one of the following is true:

- OCI is responsible for interpreting the generated routing condition.
- The scenario depends on multiple resources reconciling together.
- The scenario depends on controller watches reacting to follow-up changes.
- The failure mode would likely only appear against real infrastructure.

Keep scenarios in unit tests when they mainly prove:

- pure condition-mapping logic,
- string transformation details,
- exhaustive invalid-input permutations,
- narrow helper behavior that does not need real OCI validation.

## Priority Test List

The scenarios below are listed in the recommended implementation order.

### 1. Multi-route isolation

Create two routes on the same listener and verify:

- route A serves only backend A,
- route B serves only backend B,
- deleting route A removes only route A traffic,
- route B continues to serve correctly,
- only route A programmed OCI rule names are removed.

Why this is valuable:

- proves policy rule merge and cleanup behavior on a shared listener,
- exercises route deletion without collapsing unrelated listener rules.

### 2. Backend endpoint change

Create a route to a backend service, then change the ready endpoints and verify live traffic
follows the change.

Example approaches:

- scale the backend deployment to zero and verify traffic stops matching the previous healthy echo,
- scale it back up and verify traffic recovers,
- or switch to a different ready backend identity if the fixture setup makes that easier.

Why this is valuable:

- validates EndpointSlice watch -> route reconcile -> OCI backend set update,
- covers dynamic runtime behavior that unit tests cannot prove against OCI.

### 3. Host matching

Create two routes that differ by hostname and verify:

- requests with `Host: a.example.test` reach backend A,
- requests with `Host: b.example.test` reach backend B,
- non-matching hosts do not incorrectly hit either route.

Why this is valuable:

- OCI interprets the generated host header condition,
- unit tests can validate generated strings but not real LB behavior.

### 4. Header matching variants

Create one shared scenario that provisions several static backends and three related routes on the
same listener:

- one route using an exact header match,
- one route using a supported regular-expression form that maps to OCI starts-with,
- one route using a supported regular-expression form that maps to OCI ends-with.

Example regexp forms:

- starts-with: `^foo`, `^foo.*`, `^foo\\..*`
- ends-with: `foo$`, `.*foo$`

Verify with live traffic that:

- each matching request reaches the intended backend,
- requests with the header omitted do not hit any of the header-specific backends,
- requests with the wrong value do not fall through into one of the other header routes,
- the exact, starts-with, and ends-with variants remain distinguishable by backend identity.

Why this is valuable:

- amortizes slow live setup across three closely related OCI condition behaviors,
- proves real header-condition behavior in OCI, not just local mapping,
- validates the controller's regex-compatibility contract against actual OCI routing behavior.

### 5. Path exact vs prefix

Create a small scenario that proves OCI respects the behavioral difference between:

- `Exact`
- `PathPrefix`

Example:

- exact route for `/echo`,
- prefix route for `/echo-prefix`,
- verify `/echo/extra` does not match the exact route,
- verify `/echo-prefix/extra` does match the prefix route.

Why this is valuable:

- this is a representative routing-semantics check,
- it avoids building a large path-match matrix while still validating real OCI behavior.

## Lower Priority But Still Useful

These scenarios remain useful, but they are less critical than the list above:

- HTTPS listener with TLS `Secret`,
- TLS secret rotation,
- multi-listener attachment behavior,
- one representative negative unsupported-match scenario.

They should be addressed after the routing-rule and dynamic-backend coverage above.

## Harness Gaps To Address

The current live probe helper is too limited for several routing-rule scenarios.

The combined header-matching scenario now relies on:

- HTTPRoute fixture helpers that can express header-based matches without inlining raw objects in
  each test,
- distinct static backend responses to assert backend identity explicitly.

Remaining useful harness improvements:

- optional reusable helpers for negative-match probing where a request must not hit any of the
  header-specific backends.

## Execution Notes

To keep the suite maintainable:

- prefer one representative live scenario per supported routing feature,
- avoid cloning the unit-test matrix into e2e,
- keep each test focused on a distinct integration risk,
- implement and stabilize scenarios one by one.
