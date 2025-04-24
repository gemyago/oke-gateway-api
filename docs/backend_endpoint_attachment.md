# OCI Backend Endpoint Attachment Design

This document outlines the design for the `oke-gateway-api` controller logic responsible for managing backend servers (endpoints) in an OCI Load Balancer's Backend Set based on the state of Kubernetes `EndpointSlice` resources corresponding to `HTTPRoute` backend references.

**Prerequisite:** This design assumes that the core reconciliation logic for `GatewayClass`, `Gateway`, and `HTTPRoute` (including parent resolution and route acceptance) is already handled, as described in `docs/core_reconciliation.md`. The entry point for this logic is typically within the `httpRouteModel.programRoute` function (or a similar function called during `HTTPRoute` reconciliation).

## Goals

*   Dynamically update OCI Load Balancer Backend Sets with the IP addresses and ports of **ready** Pods backing Kubernetes Services referenced by accepted `HTTPRoute` resources.
*   Ensure traffic is routed only to healthy Pod endpoints by synchronizing the OCI Backend Set with relevant `EndpointSlice` data.
*   Reflect the backend attachment status accurately on the `HTTPRoute` resource.

## Background

*   **Kubernetes Service Discovery:** `EndpointSlice` resources contain the actual IP addresses, ports, and readiness conditions of Pods backing a `Service`.
*   **OCI Load Balancer:** OCI Flexible Load Balancers use `Backend Set` resources to group backend servers. Each `Backend` within a set is defined by an IP address and port. OCI health checks configured on the Backend Set determine the runtime health of these backends.

## Proposed Design

The controller utilizes a dedicated `HTTPBackendModel` component to synchronize OCI Backend Sets. This logic is triggered by the `HTTPRouteController`'s reconciliation loop, which itself runs in response to changes in `HTTPRoute` or relevant `EndpointSlice` resources.

### Watched Resources & Reconciliation Triggers

The endpoint attachment logic needs to run whenever:

1.  An `HTTPRoute` referencing backend Services is created or updated.
2.  An `EndpointSlice` associated with a Service referenced in an `HTTPRoute`'s `backendRefs` changes (e.g., Pod starts/stops, readiness changes).

To achieve the second trigger, the controller manager setup (`start_manager.go`) **watches `EndpointSlice` resources**. It uses a mapping function (e.g., `handler.EnqueueRequestsFromMapFunc`) to determine which `HTTPRoute`(s) are affected by an `EndpointSlice` change and enqueues those routes for reconciliation by the `HTTPRouteController`.

### Reconciliation Flow

1.  **Trigger:** `HTTPRouteController.Reconcile` is invoked for an `HTTPRoute` (due to direct change or related `EndpointSlice` change).
2.  **Resolve/Accept:** The controller uses `httpRouteModel` to resolve parent `Gateway` details and accept the route (setting status conditions).
3.  **Delegate to Model:** `httpRouteModel.programRoute` (or similar) calls `httpBackendModel.SyncBackendEndpoints`, passing the `ctx`, the accepted `HTTPRoute`, and resolved `Gateway` details (containing OCI LB info).
4.  **Sync Backends (`httpBackendModel.SyncBackendEndpoints`):**
    *   Iterates through each rule/backendRef in the `HTTPRoute`.
    *   Determines the target OCI Load Balancer **Backend Set OCID/Name** based on convention and Gateway info.
    *   Calls internal method `resolveReadyEndpoints` to fetch relevant `EndpointSlice` resources for the backend `Service` and filter for ready/serving/non-terminating endpoints matching the correct port and IP family.
    *   Constructs the complete list of desired OCI `BackendDetails` objects.
    *   Calls the OCI SDK `UpdateBackendSet` operation, providing the full list of desired backends and preserving necessary existing settings (like HealthChecker, Policy).
    *   Returns success or error.
5.  **Update Status:** `httpRouteModel` receives the result from `SyncBackendEndpoints` and updates the `HTTPRoute` status conditions accordingly.

### OCI API Interaction

*   Handled primarily by the `HTTPBackendModel`.
*   Uses the OCI SDK for Go.
*   Authenticates using instance principals or API keys.
*   Targets the correct OCI region and compartment.
*   Required API calls for endpoint attachment:
    *   `UpdateBackendSet` (primary operation)
    *   (Potentially `GetBackendSet`, `CreateBackendSet` if managing lifecycle, `GetLoadBalancer`, `GetListener` for context, OCIDs, and existence checks).

### Status Updates

*   The `httpRouteModel` updates the `HTTPRoute` status conditions based on the success/failure result returned by the `HTTPBackendModel`.
*   Errors from the OCI `UpdateBackendSet` call should be reflected in the status message.

### Error Handling

*   The `HTTPBackendModel` should handle retries for transient OCI API errors (`UpdateBackendSet`).
*   Errors like "BackendSetNotFound" should be handled gracefully (potentially attempting creation if responsible, or returning a specific error type).
*   Errors are propagated back to the `httpRouteModel` to be reflected in the `HTTPRoute` status.
*   Clear logging within the `HTTPBackendModel` is essential.

## Implementation Plan (TDD Approach)

This plan outlines the steps to implement the backend endpoint attachment logic using the `HTTPBackendModel` component.

1.  **Define `HTTPBackendModel` Interface & Struct:**
    *   Define an interface (e.g., `HTTPBackendModel`) with the `SyncBackendEndpoints(ctx context.Context, route *gatewayv1.HTTPRoute, gatewayDetails *app.ResolvedGatewayDetails) error` method (adjust `gatewayDetails` type as needed).
    *   Define the concrete struct (e.g., `httpBackendModel`) implementing this interface, holding dependencies (`k8sClient`, `ociClient`, `logger`).
    *   Integrate this component into the application's dependency injection setup (e.g., `dig`).
    *   *(TDD: Create interface/struct. Create mocks using `mockery` for the interface.)*

2.  **Setup `EndpointSlice` Watch:**
    *   Modify `internal/k8s/start_manager.go` to add a watch for `discoveryv1.EndpointSlice`.
    *   Implement the `handler.EventHandler` (specifically `handler.EnqueueRequestsFromMapFunc`) to map `EndpointSlice` changes to `HTTPRoute` reconcile requests based on the `kubernetes.io/service-name` label and `HTTPRoute` backendRefs.
    *   *(TDD: Test the mapping logic, likely needing a fake client accessible to the handler.)*

3.  **Modify `HTTPRouteModel` (`programRoute`):**
    *   Inject the `HTTPBackendModel` dependency into `httpRouteModel`.
    *   In `programRoute` (after successfully resolving backend references), replace the placeholder/future backend logic with a call to `c.httpBackendModel.SyncBackendEndpoints(ctx, httpRoute, resolvedData.gatewayDetails)` (adjust variable names).
    *   Handle the error returned by `SyncBackendEndpoints` and use it to determine the status update for the `HTTPRoute` (e.g., setting `RouteConditionResolvedRefs` status).
    *   *(TDD: Update `programRoute` tests. Mock the `HTTPBackendModel` dependency. Verify that `SyncBackendEndpoints` is called with the correct arguments. Verify `HTTPRoute` status updates based on the mocked return value of `SyncBackendEndpoints`.)*

4.  **Implement Endpoint Resolution within `HTTPBackendModel`:**
    *   Create a (likely private) method within `httpBackendModel` like `resolveReadyEndpoints(ctx context.Context, serviceNamespace string, serviceName string, servicePort int32) ([]loadbalancer.BackendDetails, error)` (adjust types as needed, especially for `BackendDetails`).
    *   **(TDD):** Write unit tests for `resolveReadyEndpoints`:
        *   Use a mock `k8sClient`.
        *   Test listing of `EndpointSlices` with correct label selectors.
        *   Test filtering logic (Ready, Serving, Not Terminating conditions, Port matching, IP family).
        *   Test conversion to the required OCI `BackendDetails` struct format.
    *   **Implement:** Implement the method using the `k8sClient` dependency.

5.  **Implement OCI Sync within `HTTPBackendModel.SyncBackendEndpoints`:**
    *   **(TDD):** Write unit tests for the `SyncBackendEndpoints` method:
        *   Mock the `k8sClient` (to mock the results of internal calls to `resolveReadyEndpoints`).
        *   Mock the `ociClient` (interface wrapping OCI SDK calls).
        *   For each rule/backendRef in the input `HTTPRoute`:
            *   Verify the target Backend Set Name/OCID is correctly determined.
            *   Verify `resolveReadyEndpoints` is called.
            *   Setup expectations on the mock `ociClient` for `UpdateBackendSet` to be called with the correct LoadBalancer/BackendSet identifiers and the list of `BackendDetails` from the mocked `resolveReadyEndpoints`. Ensure necessary fields (Policy, HealthChecker) are included (mock `GetBackendSet` if needed).
            *   Test error handling from OCI calls.
    *   **Implement:** Implement `SyncBackendEndpoints`. It will iterate through rules/backendRefs, call `resolveReadyEndpoints`, determine the target OCI Backend Set, potentially fetch existing details (`GetBackendSet`), construct `UpdateBackendSetDetails` with the full backend list, and call `ociClient.UpdateBackendSet`.

6.  **Add E2E Test (Optional but Recommended):**
    *   Create an end-to-end test scenario involving deploying a sample application, creating `GatewayClass`, `Gateway`, `HTTPRoute`, and verifying OCI Backend Set contents via the OCI API as Pods scale or change readiness.

## Future Considerations (Specific to Attachment)

*   **Backend Set Management:** Define clearly whether the controller creates/manages Backend Sets (including `HealthChecker` and `Policy`) or assumes they exist. If creating, determine how configuration is derived (defaults, annotations, policy).
*   **Weight/Backup Support:** If needed, ensure the `BackendDetails` objects passed to `UpdateBackendSet` include the correct `weight` or `backup` fields based on `HTTPRoute` filters or `Service` annotations.
*   **Graceful Termination (Draining):** The `UpdateBackendSet` approach implicitly handles draining if OCI's LB performs it automatically when backends are removed. However, explicit draining control might still require watching the `terminating` condition and potentially making an `UpdateBackend` call *before* the `UpdateBackendSet` call that removes the backend, although this adds complexity. 