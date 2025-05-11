package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/types"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type resolvedRouteDetails struct {
	gatewayDetails   resolvedGatewayDetails
	httpRoute        gatewayv1.HTTPRoute
	matchedRef       gatewayv1.ParentReference
	matchedListeners []gatewayv1.Listener
}

type resolveBackendRefsParams struct {
	httpRoute gatewayv1.HTTPRoute
}

type programRouteParams struct {
	gateway          gatewayv1.Gateway
	config           types.GatewayConfig
	httpRoute        gatewayv1.HTTPRoute
	knownBackends    map[string]v1.Service
	matchedListeners []gatewayv1.Listener
}

type setProgrammedParams struct {
	httpRoute    gatewayv1.HTTPRoute
	gatewayClass gatewayv1.GatewayClass
	gateway      gatewayv1.Gateway
	matchedRef   gatewayv1.ParentReference
}

// httpRouteModel defines the interface for managing HTTPRoute resources.
type httpRouteModel interface {
	// resolveRequest resolves the parent details for a given HTTPRoute.
	// It returns a map of parent names (gateway names) to resolved route details.
	resolveRequest(
		ctx context.Context,
		req reconcile.Request,
	) (map[apitypes.NamespacedName]resolvedRouteDetails, error)

	// acceptRoute accepts a reconcile request for a given HTTPRoute.
	// It returns updated HTTPRoute with status parents updated.
	acceptRoute(
		ctx context.Context,
		routeDetails resolvedRouteDetails,
	) (*gatewayv1.HTTPRoute, error)

	// resolveBackendRefs resolves the backend references for a given HTTPRoute.
	// It returns a map of service name to service object. It may update the route status
	// with the ResolvedRefs condition.
	resolveBackendRefs(
		ctx context.Context,
		params resolveBackendRefsParams,
	) (map[string]v1.Service, error)

	// isProgrammingRequired checks if the route programming is required based on the current state.
	isProgrammingRequired(
		details resolvedRouteDetails,
	) (bool, error)

	// programRoute programs a given HTTPRoute.
	programRoute(
		ctx context.Context,
		params programRouteParams,
	) error

	// setProgrammed marks the route as successfully programmed by updating its status.
	setProgrammed(
		ctx context.Context,
		params setProgrammedParams,
	) error
}

// parentRefSameTarget checks if two parent references target the same resource.
// It ignores the section name and port.
func parentRefSameTarget(a, b gatewayv1.ParentReference) bool {
	return a.Name == b.Name &&
		lo.FromPtr(a.Namespace) == lo.FromPtr(b.Namespace) &&
		lo.FromPtr(a.Kind) == lo.FromPtr(b.Kind) &&
		lo.FromPtr(a.Group) == lo.FromPtr(b.Group)
}

// makeTargetOnlyParentRef makes a parent reference that only targets the resource
// by name and namespace. It ignores the section name and port.
func makeTargetOnlyParentRef(parentRef gatewayv1.ParentReference) gatewayv1.ParentReference {
	return gatewayv1.ParentReference{
		Name:      parentRef.Name,
		Namespace: parentRef.Namespace,
		Kind:      parentRef.Kind,
		Group:     parentRef.Group,
	}
}

func backendRefName(
	backendRef gatewayv1.HTTPBackendRef,
	defaultNamespace string,
) apitypes.NamespacedName {
	return apitypes.NamespacedName{
		Name: string(backendRef.BackendObjectReference.Name),
		Namespace: lo.IfF(
			backendRef.BackendObjectReference.Namespace != nil,
			func() string { return string(*backendRef.BackendObjectReference.Namespace) },
		).Else(defaultNamespace),
	}
}

type httpRouteModelImpl struct {
	client               k8sClient
	logger               *slog.Logger
	gatewayModel         gatewayModel
	resourcesModel       resourcesModel
	ociLoadBalancerModel ociLoadBalancerModel
}

// resolveRouteParentRefData attempts to resolve a single parent reference for an HTTPRoute.
// It returns the resolved gateway details, the matched listeners based on SectionName (if any),
// and an error if resolution fails. If the gateway is not found or no listeners match the
// SectionName, it returns nil details/listeners without an error.
func (m *httpRouteModelImpl) resolveRouteParentRefData(
	ctx context.Context,
	httpRoute gatewayv1.HTTPRoute,
	parentRef gatewayv1.ParentReference,
	defaultNamespace string,
) (*resolvedGatewayDetails, []gatewayv1.Listener, error) {
	var resolvedGatewayData resolvedGatewayDetails
	gatewayNamespace := defaultNamespace
	if parentRef.Namespace != nil {
		gatewayNamespace = string(lo.FromPtr(parentRef.Namespace))
	}
	parentName := apitypes.NamespacedName{
		Namespace: gatewayNamespace,
		Name:      string(parentRef.Name),
	}
	m.logger.DebugContext(ctx, "Resolving parent for HTTProute",
		slog.String("parentName", parentName.String()),
		slog.Any("parentRef", parentRef),
		slog.String("route", apitypes.NamespacedName{
			Namespace: httpRoute.Namespace,
			Name:      httpRoute.Name,
		}.String()),
	)

	gatewayResolved, err := m.gatewayModel.resolveReconcileRequest(ctx, reconcile.Request{
		NamespacedName: parentName,
	}, &resolvedGatewayData)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve gateway %s for route %s/%s: %w",
			parentName.String(), httpRoute.Namespace, httpRoute.Name, err)
	}
	if !gatewayResolved {
		m.logger.DebugContext(ctx, "Gateway not resolved or not relevant",
			slog.String("parentName", parentName.String()),
		)
		return nil, nil, nil
	}

	if parentRef.SectionName != nil {
		sectionName := *parentRef.SectionName
		matchingListeners := lo.Filter(resolvedGatewayData.gateway.Spec.Listeners, func(l gatewayv1.Listener, _ int) bool {
			return l.Name == sectionName
		})

		if len(matchingListeners) == 0 {
			m.logger.DebugContext(ctx, "Gateway resolved, but no listener matched section name",
				slog.String("parentName", parentName.String()),
				slog.String("sectionName", string(sectionName)),
			)
			return nil, nil, nil
		}

		m.logger.DebugContext(ctx, "Gateway resolved with matching section name listener(s)",
			slog.String("parentName", parentName.String()),
			slog.String("sectionName", string(sectionName)),
			slog.Int("matchedListenersCount", len(matchingListeners)),
		)
		return &resolvedGatewayData, matchingListeners, nil
	}

	// If no SectionName, all listeners are considered matched
	m.logger.DebugContext(ctx, "Gateway resolved without section name, all listeners match",
		slog.String("parentName", parentName.String()),
	)
	return &resolvedGatewayData, resolvedGatewayData.gateway.Spec.Listeners, nil
}

// aggregateRouteParentRefData adds or updates the results map with the resolved parent details.
// It handles merging listeners if the same gateway is referenced multiple times (e.g., by different sections).
func (m *httpRouteModelImpl) aggregateRouteParentRefData(
	ctx context.Context,
	results map[apitypes.NamespacedName]resolvedRouteDetails,
	httpRoute gatewayv1.HTTPRoute,
	gatewayDetails resolvedGatewayDetails,
	matchedRef gatewayv1.ParentReference, // Should be target-only ref
	matchedListeners []gatewayv1.Listener,
) {
	parentName := apitypes.NamespacedName{
		Namespace: gatewayDetails.gateway.Namespace,
		Name:      gatewayDetails.gateway.Name,
	}

	if existingResult, found := results[parentName]; found {
		newListeners := lo.UniqBy(
			append(existingResult.matchedListeners, matchedListeners...),
			func(l gatewayv1.Listener) gatewayv1.SectionName {
				return l.Name
			},
		)
		existingResult.matchedListeners = newListeners
		results[parentName] = existingResult
		m.logger.DebugContext(ctx, "Appended/merged listeners for existing gateway result",
			slog.String("parentName", parentName.String()),
			slog.Int("totalListeners", len(newListeners)),
		)
	} else {
		results[parentName] = resolvedRouteDetails{
			httpRoute:        httpRoute,
			gatewayDetails:   gatewayDetails,
			matchedRef:       matchedRef, // Use the target-only ref
			matchedListeners: matchedListeners,
		}
		m.logger.DebugContext(ctx, "Added new gateway result",
			slog.String("parentName", parentName.String()),
			slog.Int("initialListeners", len(matchedListeners)),
		)
	}
}

func (m *httpRouteModelImpl) resolveRequest(
	ctx context.Context,
	req reconcile.Request,
) (map[apitypes.NamespacedName]resolvedRouteDetails, error) {
	var httpRoute gatewayv1.HTTPRoute
	if err := m.client.Get(ctx, req.NamespacedName, &httpRoute); err != nil {
		if apierrors.IsNotFound(err) {
			m.logger.DebugContext(ctx, "HTTProute not found during resolution",
				slog.String("route", req.NamespacedName.String()),
			)
			return map[apitypes.NamespacedName]resolvedRouteDetails{}, nil
		}
		return nil, fmt.Errorf("failed to get HTTPRoute %s: %w", req.NamespacedName.String(), err)
	}

	results := make(map[apitypes.NamespacedName]resolvedRouteDetails)

	for _, parentRef := range httpRoute.Spec.ParentRefs {
		resolvedGatewayData, matchedListeners, err := m.resolveRouteParentRefData(
			ctx,
			httpRoute,
			parentRef,
			req.NamespacedName.Namespace,
		)
		if err != nil {
			return nil, err
		}

		if resolvedGatewayData != nil {
			m.aggregateRouteParentRefData(ctx,
				results,
				httpRoute,
				*resolvedGatewayData,
				makeTargetOnlyParentRef(parentRef),
				matchedListeners,
			)
		}
	}

	if len(results) == 0 {
		m.logger.InfoContext(ctx, "No relevant gateway found for HTTProute after checking all parents",
			slog.String("route", req.NamespacedName.String()),
			slog.Int("triedParentRefs", len(httpRoute.Spec.ParentRefs)),
		)
	}

	return results, nil
}

// TODO: Some mechanism to check if all parents are accepted
// also if listeners are present

func (m *httpRouteModelImpl) acceptRoute(
	ctx context.Context,
	routeDetails resolvedRouteDetails,
) (*gatewayv1.HTTPRoute, error) {
	parentStatus, parentStatusIndex, found := lo.FindIndexOf(
		routeDetails.httpRoute.Status.Parents,
		func(s gatewayv1.RouteParentStatus) bool {
			return s.ControllerName == routeDetails.gatewayDetails.gatewayClass.Spec.ControllerName &&
				parentRefSameTarget(s.ParentRef, routeDetails.matchedRef)
		})
	if found {
		existingCondition := meta.FindStatusCondition(
			parentStatus.Conditions,
			string(gatewayv1.RouteConditionAccepted),
		)
		if existingCondition != nil &&
			existingCondition.ObservedGeneration == routeDetails.httpRoute.Generation &&
			existingCondition.Status == metav1.ConditionTrue {
			m.logger.DebugContext(ctx, "HTTProute is already accepted",
				slog.String("route", routeDetails.httpRoute.Name),
				slog.String("gateway", routeDetails.gatewayDetails.gateway.Name),
				slog.Int64("generation", existingCondition.ObservedGeneration),
			)
			return &routeDetails.httpRoute, nil
		}
	} else {
		parentStatus = gatewayv1.RouteParentStatus{
			// We collapse the parent ref into a single object
			// so using just name and namespace
			ParentRef:      makeTargetOnlyParentRef(routeDetails.matchedRef),
			ControllerName: routeDetails.gatewayDetails.gatewayClass.Spec.ControllerName,
		}
	}

	httpRoute := routeDetails.httpRoute.DeepCopy()
	meta.SetStatusCondition(&parentStatus.Conditions, metav1.Condition{
		Type:               string(gatewayv1.RouteConditionAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             string(gatewayv1.RouteReasonAccepted),
		ObservedGeneration: httpRoute.Generation,
		LastTransitionTime: metav1.Now(),
		Message:            fmt.Sprintf("Route accepted by %s", routeDetails.gatewayDetails.gateway.Name),
	})

	if found {
		m.logger.InfoContext(ctx, "Updating HTTProute status as Accepted",
			slog.String("route", httpRoute.Name),
			slog.String("gateway", routeDetails.gatewayDetails.gateway.Name),
		)
		httpRoute.Status.Parents[parentStatusIndex] = parentStatus
	} else {
		m.logger.InfoContext(ctx, "Accepting new HTTProute",
			slog.String("route", httpRoute.Name),
			slog.String("gateway", routeDetails.gatewayDetails.gateway.Name),
		)
		httpRoute.Status.Parents = append(httpRoute.Status.Parents, parentStatus)
	}

	if err := m.client.Status().Update(ctx, httpRoute); err != nil {
		return nil, fmt.Errorf("failed to update status for HTTProute %s: %w", httpRoute.Name, err)
	}

	return httpRoute, nil
}

func (m *httpRouteModelImpl) resolveBackendRefs(
	ctx context.Context,
	params resolveBackendRefsParams,
) (map[string]v1.Service, error) {
	resolvedBackendRefs := make(map[string]v1.Service)
	for _, rule := range params.httpRoute.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			fullName := backendRefName(backendRef, params.httpRoute.Namespace)

			var service v1.Service
			if err := m.client.Get(ctx, fullName, &service); err != nil {
				return nil, fmt.Errorf("failed to get service %s: %w", fullName.String(), err)
			}

			m.logger.DebugContext(ctx, "Backend ref resolved",
				slog.String("fullName", fullName.String()),
				slog.String("uuid", string(service.UID)),
			)
			resolvedBackendRefs[fullName.String()] = service

			// TODO: Maybe check port and other stuff here
		}
	}

	// TODO: This should handle unresolved refs and update the status
	// as per spec

	return resolvedBackendRefs, nil
}

func (m *httpRouteModelImpl) programRoute(
	ctx context.Context,
	params programRouteParams,
) error {
	for key, service := range params.knownBackends {
		err := m.ociLoadBalancerModel.reconcileBackendSet(ctx, reconcileBackendSetParams{
			loadBalancerID: params.config.Spec.LoadBalancerID,
			service:        service,
		})
		if err != nil {
			return fmt.Errorf("failed to reconcile backend set for service %s: %w", key, err)
		}
	}

	// Resolve and tidy matching policies
	matchingListenerPolicies := make([]loadbalancer.RoutingPolicy, len(params.matchedListeners))
	for i, listener := range params.matchedListeners {
		routingPolicy, err := m.ociLoadBalancerModel.resolveAndTidyRoutingPolicy(ctx, resolveAndTidyRoutingPolicyParams{
			loadBalancerID: params.config.Spec.LoadBalancerID,
			policyName:     listenerPolicyName(string(listener.Name)),
			httpRoute:      params.httpRoute,
		})
		if err != nil {
			return fmt.Errorf("failed to resolve and tidy routing policy for listener %s: %w", listener.Name, err)
		}
		matchingListenerPolicies[i] = routingPolicy
	}

	for ruleIndex := range params.httpRoute.Spec.Rules {
		for policyIndex := range matchingListenerPolicies {
			var updatedRules []loadbalancer.RoutingRule
			updatedRules, err := m.ociLoadBalancerModel.upsertRoutingRule(ctx, upsertRoutingRuleParams{
				actualPolicyRules:  matchingListenerPolicies[policyIndex].Rules,
				httpRoute:          params.httpRoute,
				httpRouteRuleIndex: ruleIndex,
			})
			if err != nil {
				return fmt.Errorf("failed to reconcile routing rule %d for route %s: %w", ruleIndex, params.httpRoute.Name, err)
			}
			matchingListenerPolicies[policyIndex].Rules = updatedRules
		}
	}

	// We commit in the end after all rules are added, otherwise
	// we may be doing to many updates to the same policy
	for _, policy := range matchingListenerPolicies {
		err := m.ociLoadBalancerModel.commitRoutingPolicy(ctx, commitRoutingPolicyParams{
			loadBalancerID: params.config.Spec.LoadBalancerID,
			policy:         policy,
		})
		if err != nil {
			return fmt.Errorf("failed to commit routing policy %s: %w", lo.FromPtr(policy.Name), err)
		}
	}

	// TODO: Cleanup backend sets that are no longer referenced

	return nil
}

func (m *httpRouteModelImpl) isProgrammingRequired(
	details resolvedRouteDetails,
) (bool, error) {
	parentStatus, found := lo.Find(details.httpRoute.Status.Parents, func(s gatewayv1.RouteParentStatus) bool {
		return s.ControllerName == details.gatewayDetails.gatewayClass.Spec.ControllerName &&
			parentRefSameTarget(s.ParentRef, details.matchedRef)
	})

	if !found {
		return true, nil
	}

	return !m.resourcesModel.isConditionSet(isConditionSetParams{
		resource:      &details.httpRoute,
		conditions:    parentStatus.Conditions,
		conditionType: string(gatewayv1.RouteConditionResolvedRefs),
		annotations: map[string]string{
			HTTPRouteProgrammingRevisionAnnotation: HTTPRouteProgrammingRevisionValue,
		},
	}), nil
}

func (m *httpRouteModelImpl) setProgrammed(
	ctx context.Context,
	params setProgrammedParams,
) error {
	httpRoute := params.httpRoute.DeepCopy()

	_, statusIndex, found := lo.FindIndexOf(
		httpRoute.Status.Parents,
		func(s gatewayv1.RouteParentStatus) bool {
			return s.ControllerName == params.gatewayClass.Spec.ControllerName &&
				parentRefSameTarget(s.ParentRef, params.matchedRef)
		},
	)

	if !found {
		return fmt.Errorf("parent status not found for controller %s and parentRef %s",
			params.gatewayClass.Spec.ControllerName,
			params.matchedRef.Name,
		)
	}

	if err := m.resourcesModel.setCondition(ctx, setConditionParams{
		resource:      httpRoute,
		conditions:    &httpRoute.Status.Parents[statusIndex].Conditions,
		conditionType: string(gatewayv1.RouteConditionResolvedRefs),
		status:        metav1.ConditionTrue,
		reason:        string(gatewayv1.RouteReasonResolvedRefs),
		message:       fmt.Sprintf("Route programmed by %s", params.gateway.Name),
		annotations: map[string]string{
			HTTPRouteProgrammingRevisionAnnotation: HTTPRouteProgrammingRevisionValue,
		},
	}); err != nil {
		return fmt.Errorf("failed to update programmed status for HTTProute %s: %w", httpRoute.Name, err)
	}

	return nil
}

// httpRouteModelDeps defines the dependencies required for the httpRouteModel.
type httpRouteModelDeps struct {
	dig.In

	K8sClient      k8sClient
	RootLogger     *slog.Logger
	GatewayModel   gatewayModel
	OciLBModel     ociLoadBalancerModel
	ResourcesModel resourcesModel
}

// newHTTPRouteModel creates a new instance of httpRouteModel.
func newHTTPRouteModel(deps httpRouteModelDeps) httpRouteModel {
	return &httpRouteModelImpl{
		client:               deps.K8sClient,
		logger:               deps.RootLogger.WithGroup("httproute-model"),
		gatewayModel:         deps.GatewayModel,
		ociLoadBalancerModel: deps.OciLBModel,
		resourcesModel:       deps.ResourcesModel,
	}
}
