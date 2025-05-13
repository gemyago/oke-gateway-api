# Kubernetes Gateway API to OCI Load Balancer Mapping

This document summarizes how the `oke-gateway-api` controller maps Kubernetes Gateway API resources to OCI Load Balancer components. The controller watches K8s resources (`GatewayClass`, `Gateway`, `HTTPRoute`, `Service`, `EndpointSlice`) and configures an OCI Load Balancer based.

## Resource Mapping

1.  **`GatewayClass`**: Identifies resources managed by this controller. No direct OCI mapping; status is updated to `Accepted=True`.

2.  **`GatewayConfig` (CRD)**: Links `Gateway` to a specific OCI Load Balancer via `spec.loadBalancerId` (containing the LB OCID).

3.  **`Gateway`**: Defines entry points.
    *   **`spec.listeners` -> OCI Listeners (`oci.loadbalancer.Listener`)**: Port and protocol are mapped. A default OCI Backend Set and an OCI Routing Policy (initially with a default rule) are created per listener.
    *   **`spec.addresses`**: Not used as the LB IP is pre-existing.

4.  **`HTTPRoute`**: Defines routing logic and backends.
    *   Attaches to a `Gateway` via `spec.parentRefs`.
    *   Each distinct backend ref in a route translates to a `Service` resource and a corresponding OCI Backend Set.
    *   **`spec.rules[]`**: Each rule maps to:
        *   **OCI Routing Rule (`oci.loadbalancer.RoutingRule`)**: HTTPRouteRule `matches` (path, headers) become OCI Routing Rule `conditions` within the Listener's Routing Policy/RuleSet. The action forwards to the rule's dedicated Backend Set. Rule order is preserved.
    *   **`rule.backendRefs[]` (pointing to `Service`) -> OCI Backends (`oci.BackendDetails`)**:
        *   The controller watches `EndpointSlice`s for the referenced `Service`.
        *   **Ready** Pod IPs/ports from `EndpointSlice`s dynamically populate the `backends` list in the corresponding rule-specific OCI Backend Set.

## Reconciliation Flow Summary

1.  Controller identifies its `GatewayClass`.
2.  `GatewayConfig` provides the OCI LB OCID.
3.  `Gateway` reconciliation creates/updates OCI Listeners, a default Backend Set, and Routing Policies.
4.  `HTTPRoute` reconciliation creates/updates rule-specific OCI Backend Sets and adds corresponding rules (conditions + forward action) to the Listener's Routing Policy/RuleSet. It also starts watching associated `EndpointSlice`s.
5.  `EndpointSlice` changes trigger updates to the OCI Backend Set's backend list (Pod IPs/ports).

This mapping allows Kubernetes Gateway API resources to define complex routing rules and backend configurations declaratively, which are then translated into the necessary OCI Load Balancer configuration. 