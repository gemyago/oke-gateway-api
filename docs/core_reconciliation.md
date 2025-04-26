# Core Resource Reconciliation Design

This document describes the existing reconciliation logic implemented in the `oke-gateway-api` controller for managing the core lifecycle of `GatewayClass`, `Gateway`, and `HTTPRoute` resources, up to the point where specific backend programming (like endpoint attachment) is required.

## GatewayClass Reconciliation (`gatewayclass_controller.go`)

1.  **Watch:** The controller watches `gatewayv1.GatewayClass` resources.
2.  **Filter:** It ignores `GatewayClass` resources where `spec.ControllerName` does not match the controller's designated name (`oke-gateway-api.jenyay.io`).
3.  **Acceptance:** If a matching `GatewayClass` is found and doesn't already have the `Accepted` condition set to `True`, the controller updates its status:
    *   Sets the `GatewayClassConditionStatusAccepted` condition to `True`.
    *   Reason: `GatewayClassReasonAccepted`.
    *   Message: Indicates acceptance by the controller.

## Gateway Reconciliation (`gateway_controller.go`, `gateway_model.go`)

1.  **Watch:** The controller watches `gatewayv1.Gateway` resources.
2.  **Resolve:** On a reconcile request, it resolves the `Gateway`'s details (`gatewayModel.resolveReconcileRequest`):
    *   Fetches the `Gateway` resource.
    *   Fetches the referenced `GatewayClass`.
    *   Verifies the `GatewayClass` matches the controller's name.
    *   Fetches associated configuration (e.g., from a referenced ConfigMap or CRD).
    *   Handles errors by setting appropriate status conditions on the `Gateway` (e.g., `GatewayConditionAccepted=False` if `GatewayClass` is invalid).
3.  **Acceptance:** If the `Gateway` is valid and not yet accepted, it updates the `Gateway` status:
    *   Sets the `GatewayConditionAccepted` condition to `True`.
    *   Reason: `GatewayReasonAccepted`.
4.  **Programming:** If the `Gateway` is accepted and not yet programmed (or the generation/spec changed), it attempts to program the underlying infrastructure (`gatewayModel.programGateway`):
    *   This likely involves creating or updating the core OCI Load Balancer resource based on the `Gateway` specification (listeners, addresses, etc.).
    *   Handles errors during programming by setting the `GatewayConditionProgrammed` condition to `False` with appropriate reasons (`resourceStatusError`).
    *   On success, updates the `Gateway` status:
        *   Sets the `GatewayConditionProgrammed` condition to `True`.
        *   Reason: `GatewayReasonProgrammed`.

## HTTPRoute Initial Reconciliation (`httproute_controller.go`, `httproute_model.go`)

1.  **Watch:** The controller watches `gatewayv1.HTTPRoute` resources.
2.  **Resolve Parents:** On a reconcile request, it resolves the `HTTPRoute` details (`httpRouteModel.resolveRequest`):
    *   Fetches the `HTTPRoute` resource.
    *   Validates `spec.parentRefs`, ensuring they point to a supported `Gateway`.
    *   Fetches the parent `Gateway`(s) and their resolved details (including associated OCI Load Balancer info, likely stored during `Gateway` reconciliation).
    *   Determines if the route is relevant to this controller based on the parent `Gateway`(s).
    *   Handles errors by potentially updating `HTTPRoute` status conditions.
3.  **Acceptance:** If the `HTTPRoute` has valid parent references and is not yet accepted for a given parent, it updates the `HTTPRoute` status (`httpRouteModel.acceptRoute`):
    *   Finds or creates the `status.parents` entry for the relevant `Gateway`.
    *   Sets the `RouteConditionAccepted` condition to `True` within that parent status.
    *   Reason: `RouteReasonAccepted`.
4.  **Backend Reference Resolution:** It resolves the backend services defined in `spec.rules.backendRefs` (`httpRouteModel.resolveBackendRefs`):
    *   Validates the referenced `Service` kind and group.
    *   Potentially fetches the `Service` resource to validate existence or port details (though this step might only validate the reference itself, leaving endpoint details for later).
5.  **Programming Trigger:** Calls `httpRouteModel.programRoute` with the resolved `Gateway` details, the accepted `HTTPRoute`, and the resolved `backendRefs`.

**Note:** This document covers the logic up to the point where `httpRouteModel.programRoute` is called. The *specific actions* taken within `programRoute` to configure OCI Load Balancer routing rules and backend attachments based on `EndpointSlice` data are detailed in `docs/backend_endpoint_attachment.md`. 