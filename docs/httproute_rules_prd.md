# PRD: HTTPRoute Rules to OCI Load Balancer Mapping

## 1. Goal

To enhance the OKE Gateway API controller to translate Kubernetes Gateway API `HTTPRoute` rules (path, header, method, query parameter matching) into corresponding OCI Load Balancer Listener Rules and RuleSets, enabling path-based routing, header matching, etc., for managed load balancers.

## 2. Background

Currently, the controller reconciles `HTTPRoute` resources by creating OCI Backend Sets based on the `backendRefs`, but it does not configure the OCI Listener rules to actually *use* these backend sets based on request characteristics defined in `HTTPRoute.spec.rules`. This limits the functionality to basic hostname routing managed by the listener itself.

## 3. Requirements

- The controller must monitor `HTTPRoute` resources.
- When an `HTTPRoute` is accepted and programmed for a specific Gateway listener:
    - Identify the corresponding OCI Load Balancer Listener.
    - Get or Create an OCI `RuleSet` associated with this Listener. The RuleSet name should be deterministic based on the Listener details.
    - Translate each rule defined in `HTTPRoute.spec.rules` into one or more OCI `Rule` objects within the `RuleSet`.
        - Map `HTTPRouteMatch` criteria (`path`, `headers`, `method`, `queryParams`) to appropriate OCI `Rule` conditions (`PathMatchCondition`, `HeaderMatchCondition`, `MethodMatchCondition`, `QueryParameterMatchCondition`).
        - Map `HTTPRouteRule.backendRefs` to an OCI `ForwardToBackendSetAction` within the `Rule`, using the backend set name already derived by the controller. (Initially, target the first backendRef).
        - Handle the case where one `HTTPRouteRule` has multiple `HTTPRouteMatch` entries (OR logic) by creating separate OCI `Rule`s for each match, all pointing to the same action/backend set.
        - Preserve the order of rules as defined in `HTTPRoute.spec.rules` when creating/updating OCI `Rule`s in the `RuleSet`.
    - Update the OCI `RuleSet` with the generated rules.
    - Ensure the OCI Listener is configured to use this `RuleSet`.
- Status conditions on the `HTTPRoute` should reflect whether the rules have been successfully programmed into the OCI Load Balancer.
- Deleting an `HTTPRoute` or removing it from a Gateway should result in the corresponding rules being removed from the OCI `RuleSet`.

## 4. Non-Goals (Initial Version)

- Support for `HTTPRoute` filters (e.g., `RequestHeaderModifier`).
- Support for weighted `backendRefs` within a single rule.
- Advanced OCI LB features not directly mappable from the core Gateway API spec.

## 5. OCI Resources Involved

- `oci-go-sdk/v65/loadbalancer`:
    - `Listener`
    - `RuleSet`
    - `Rule`
    - `PathMatchCondition`, `HeaderMatchCondition`, `MethodMatchCondition`, `QueryParameterMatchCondition`
    - `ForwardToBackendSetAction` 