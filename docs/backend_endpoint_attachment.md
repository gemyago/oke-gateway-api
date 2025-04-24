# OCI Backend Endpoint Attachment Design

This document outlines the design for the `oke-gateway-api` controller logic responsible for managing backend servers (endpoints) in an OCI Load Balancer's Backend Set based on the state of Kubernetes `EndpointSlice` resources corresponding to `HTTPRoute` backend references.

**Prerequisite:** This design assumes that the core reconciliation logic for `GatewayClass`, `Gateway`, and `HTTPRoute` (including parent resolution, route acceptance, and core route programming via `httpRouteModel.programRoute`) is already handled, as described in `docs/core_reconciliation.md`. The endpoint attachment logic described here is invoked by the `HTTPRouteController` *after* the core route programming is complete.

## Goals

*   Dynamically update OCI Load Balancer Backend Sets with the IP addresses and ports of **ready** Pods backing Kubernetes Services referenced by accepted `HTTPRoute` resources.
*   Ensure traffic is routed only to healthy Pod endpoints by synchronizing the OCI Backend Set with relevant `EndpointSlice` data.
*   Reflect the backend attachment status accurately on the `HTTPRoute` resource.

## Background

*   **Kubernetes Service Discovery:** `EndpointSlice` resources contain the actual IP addresses, ports, and readiness conditions of Pods backing a `Service`.
*   **OCI Load Balancer:** OCI Flexible Load Balancers use `Backend Set` resources to group backend servers. Each `Backend` within a set is defined by an IP address and port. OCI health checks configured on the Backend Set determine the runtime health of these backends.

## Proposed Design

The controller utilizes a dedicated `HTTPBackendModel` component interface to synchronize OCI Backend Sets. This logic is triggered by the `HTTPRouteController`'s reconciliation loop after core route programming is done.

### Watched Resources & Reconciliation Triggers

The `HTTPRouteController` needs to run its reconciliation loop whenever:

1.  An `HTTPRoute` resource is created, updated, or deleted.
2.  An `EndpointSlice` associated with a Service referenced in an `HTTPRoute`'s `backendRefs` changes.

To achieve this, the controller manager setup (`start_manager.go`):
*   **Watches `HTTPRoute` resources** directly.
*   **Watches `discoveryv1.EndpointSlice` resources**. It uses a mapping function (e.g., `handler.EnqueueRequestsFromMapFunc`) to determine which `HTTPRoute`(s) are affected by an `EndpointSlice` change and enqueues those routes for reconciliation by the `HTTPRouteController`.

Both triggers result in the `HTTPRouteController.Reconcile` function being called for the relevant `HTTPRoute`.

### Reconciliation Flow (within `HTTPRouteController.Reconcile`)

1.  **Trigger:** `HTTPRouteController.Reconcile` is invoked for an `HTTPRoute` (identified by `req.NamespacedName`).
2.  **Fetch Resource:** Fetch the current `HTTPRoute` object from the cluster/cache.
3.  **Handle Deletion:** If the `HTTPRoute` has a deletion timestamp, perform cleanup (e.g., call `httpRouteModel.deleteRoute`, potentially `httpBackendModel.cleanupBackendEndpoints` if needed) and remove the finalizer.
4.  **Check Generation:** Compare the fetched `httpRoute.Metadata.Generation` with the relevant `status.observedGeneration` (e.g., associated with the `Accepted` or `Programmed` condition).
5.  **Reconcile Spec (if generation changed):** If `httpRoute.Metadata.Generation > status.observedGeneration`:
    *   Use `httpRouteModel` to:
        *   Resolve parent `Gateway` details (`resolveRequest`).
        *   Accept the route (`acceptRoute`).
        *   Resolve backend references (`resolveBackendRefs`).
        *   Program the core route aspects (e.g., OCI LB routing rules via `programRoute`).
    *   Handle errors from the above steps and update status conditions accordingly.
    *   **If successful:** Update the `status.observedGeneration` to match `httpRoute.Metadata.Generation` along with relevant status conditions.
6.  **Sync Backends (Always, but uses current state):** *After* the spec reconciliation (if it happened) or immediately if the generation matched, the `HTTPRouteController` calls `httpBackendModel.SyncBackendEndpoints`. This function is responsible for ensuring the OCI backend set matches the *current* desired state based on the *existing* route configuration and the *latest* `EndpointSlice` data.
    *   Pass necessary parameters (e.g., `syncBackendEndpointsParams` containing context, the fetched `HTTPRoute`, resolved Gateway details from the previous step or fetched if needed, potentially resolved backend refs).
    *   The `SyncBackendEndpoints` implementation fetches the latest relevant `EndpointSlice` data for the route's backends and updates the OCI Backend Set.
7.  **Update Status:** Update `HTTPRoute` status conditions based on the overall outcome, including the result from `SyncBackendEndpoints` (e.g., setting a `BackendAttachmentProgrammed` condition or similar).

### OCI API Interaction

*   Handled primarily by the concrete implementation of the `HTTPBackendModel` interface.
*   Uses the OCI SDK for Go.
*   Authenticates using instance principals or API keys.
*   Targets the correct OCI region and compartment.
*   Required API calls for endpoint attachment:
    *   `UpdateBackendSet` (primary operation)
    *   (Potentially `GetBackendSet`, `GetLoadBalancer`, `GetListener` for context, OCIDs, and existence checks).

### Status Updates

*   The `HTTPRouteController` updates the `HTTPRoute` status conditions based on the success/failure results returned by the various model calls (`programRoute`, `SyncBackendEndpoints`).
*   The `status.observedGeneration` field should be updated when the spec reconciliation is successfully completed.
*   Errors from the OCI API calls (via the models) should be reflected in the status condition messages.

### Error Handling

*   The `HTTPBackendModel` implementation should handle retries for transient OCI API errors (`UpdateBackendSet`).
*   Errors like "BackendSetNotFound" should be handled gracefully within the model (potentially attempting creation if responsible, or returning a specific error type).
*   Errors are propagated back to the `HTTPRouteController` to be handled (e.g., logging, setting status, potentially requeueing).
*   Clear logging within the `HTTPBackendModel` implementation is essential.

## Implementation Plan (TDD Approach)

This plan outlines the steps to implement the backend endpoint attachment logic using the `HTTPBackendModel` interface and the optimized unified reconcile approach.

1.  **Define `HTTPBackendModel` Interface:**
    *   Ensure the interface `HTTPBackendModel` exists in `internal/app/httpbackend_model.go`.
    *   Define the method signature as `SyncBackendEndpoints(ctx context.Context, params syncBackendEndpointsParams) error`.
    *   Define the `syncBackendEndpointsParams` struct containing necessary fields (e.g., `gatewayv1.Gateway`, `gatewayv1.HTTPRoute`, `types.GatewayConfig`, `map[string]v1.Service`).
    *   *(TDD: Create/update interface file. Create mocks using `mockery` for the `HTTPBackendModel` interface.)*

2.  **Setup `EndpointSlice` Watch:**
    *   Modify `internal/k8s/start_manager.go` to add a watch for `discoveryv1.EndpointSlice`.
    *   Implement the `handler.EventHandler` (specifically `handler.EnqueueRequestsFromMapFunc`) to map `EndpointSlice` changes to `HTTPRoute` reconcile requests based on the `kubernetes.io/service-name` label and `HTTPRoute` backendRefs.
    *   *(TDD: Test the mapping logic, likely needing a fake client accessible to the handler.)*

3.  **Modify `HTTPRouteController.Reconcile`:**
    *   Inject the `HTTPBackendModel` dependency into `HTTPRouteController`.
    *   Fetch the `HTTPRoute` resource.
    *   Implement deletion logic.
    *   **Implement Generation Check:** Compare `httpRoute.Metadata.Generation` with `status.observedGeneration`.
    *   **Conditional Spec Reconcile:**
        *   If generation changed: Call `resolveRequest`, `acceptRoute`, `resolveBackendRefs`, `programRoute`. Handle errors. If successful, update `status.observedGeneration`.
        *   If generation unchanged: Skip the spec reconcile steps.
    *   **Call Sync Backends:** Construct `syncBackendEndpointsParams` (potentially needing to fetch/resolve Gateway info if spec reconcile was skipped) and call `r.httpBackendModel.SyncBackendEndpoints(ctx, params)`.
    *   **Update Status:** Update status conditions based on the outcomes of spec reconcile (if run) and `SyncBackendEndpoints`.
    *   *(TDD: Update `HTTPRouteController.Reconcile` tests. Mock `httpRouteModel` and `httpBackendModel`. Test both paths (generation changed vs. unchanged). Verify `programRoute` is called conditionally. Verify `SyncBackendEndpoints` is called. Verify status updates, including `observedGeneration`.)*

4.  **Implement Concrete `httpBackendModel` Struct & Constructor:**
    *   Define the concrete struct (e.g., `httpBackendModel`) implementing the `HTTPBackendModel` interface in `internal/app/httpbackend_model.go`.
    *   Define its dependencies (`k8sClient`, `ociClient`, `logger`).
    *   Implement the constructor `NewHTTPBackendModel`.
    *   Integrate this concrete implementation into the application's dependency injection setup (e.g., `dig`).
    *   *(TDD: Implement struct and constructor. Ensure DI wiring passes.)*

5.  **Implement Endpoint Resolution within `httpBackendModel`:**
    *   Create a (likely private) method within the concrete `httpBackendModel` struct: `resolveReadyEndpoints(ctx context.Context, serviceNamespace string, serviceName string, servicePort int32) ([]loadbalancer.BackendDetails, error)`.
    *   **(TDD):** Write unit tests for `resolveReadyEndpoints` using a mock `k8sClient`.
    *   **Implement:** Implement the method using the `k8sClient` dependency.

6.  **Implement OCI Sync within `httpBackendModel.SyncBackendEndpoints`:**
    *   **(TDD):** Write unit tests for the `SyncBackendEndpoints` method of the concrete `httpBackendModel` struct:
        *   Use mock `k8sClient` and mock `ociClient`.
        *   Verify `resolveReadyEndpoints` is called correctly for each backendRef.
        *   Verify OCI `UpdateBackendSet` is called with the correct parameters (including preserving existing Policy/HealthChecker if needed via `GetBackendSet`).
        *   Test error handling.
    *   **Implement:** Implement the `SyncBackendEndpoints` method logic as described in the "Reconciliation Flow" section using the `ociClient` dependency.

7.  **Add E2E Test (Optional but Recommended):**
    *   Create end-to-end tests as described previously.

## Future Considerations (Specific to Attachment)

*   **Backend Set Management:** Define clearly whether the controller creates/manages Backend Sets (including `HealthChecker` and `Policy`) or assumes they exist. If creating, determine how configuration is derived (defaults, annotations, policy).
*   **Weight/Backup Support:** If needed, ensure the `BackendDetails` objects passed to `UpdateBackendSet` include the correct `weight` or `backup` fields based on `HTTPRoute` filters or `Service` annotations.
*   **Graceful Termination (Draining):** The `UpdateBackendSet` approach implicitly handles draining if OCI's LB performs it automatically when backends are removed. However, explicit draining control might still require watching the `terminating` condition and potentially making an `UpdateBackend` call *before* the `UpdateBackendSet` call that removes the backend, although this adds complexity. 