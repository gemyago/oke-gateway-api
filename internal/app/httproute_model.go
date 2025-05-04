package app

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

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
	gateway   gatewayv1.Gateway
	config    types.GatewayConfig
	httpRoute gatewayv1.HTTPRoute
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
	// It returns a map of service name to service port. It may update the route status
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

func backendSetName(httpRoute gatewayv1.HTTPRoute, rule gatewayv1.HTTPRouteRule, ruleIndex int) string {
	ruleName := lo.TernaryF(
		rule.Name != nil,
		func() gatewayv1.SectionName { return *rule.Name },
		func() gatewayv1.SectionName { return gatewayv1.SectionName("rt-" + strconv.Itoa(ruleIndex)) },
	)
	return httpRoute.Name + "-" + string(ruleName)
}

type httpRouteModelImpl struct {
	client       k8sClient
	logger       *slog.Logger
	gatewayModel gatewayModel
	ociLoadBalancerModel
}

func (m *httpRouteModelImpl) resolveRequest(
	ctx context.Context,
	req reconcile.Request,
) (map[apitypes.NamespacedName]resolvedRouteDetails, error) {
	var httpRoute gatewayv1.HTTPRoute
	if err := m.client.Get(ctx, req.NamespacedName, &httpRoute); err != nil {
		if apierrors.IsNotFound(err) {
			m.logger.DebugContext(ctx, "HTTProute not found",
				slog.String("route", req.NamespacedName.String()),
			)
			return map[apitypes.NamespacedName]resolvedRouteDetails{}, nil
		}
		return nil, err
	}

	results := make(map[apitypes.NamespacedName]resolvedRouteDetails)

	for _, parentRef := range httpRoute.Spec.ParentRefs {
		var resolvedGatewayData resolvedGatewayDetails
		gatewayNamespace := req.NamespacedName.Namespace
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
		)
		gatewayResolved, err := m.gatewayModel.resolveReconcileRequest(ctx, reconcile.Request{
			NamespacedName: parentName,
		}, &resolvedGatewayData)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve reconcile request for gateway %s: %w", parentRef.Name, err)
		}

		if !gatewayResolved {
			continue
		}

		if parentRef.SectionName != nil {
			sectionName := *parentRef.SectionName
			matchingListeners := lo.Filter(resolvedGatewayData.gateway.Spec.Listeners, func(l gatewayv1.Listener, _ int) bool {
				return l.Name == *parentRef.SectionName
			})

			if len(matchingListeners) == 0 {
				m.logger.DebugContext(ctx, "Gateway resolved, but no listener matched section name",
					slog.String("parentName", parentName.String()),
					slog.String("sectionName", string(sectionName)),
				)
				continue
			}

			m.logger.DebugContext(ctx, "Gateway resolved with matching section name listener(s)",
				slog.String("parentName", parentName.String()),
				slog.String("sectionName", string(sectionName)),
				slog.Int("matchedListenersCount", len(matchingListeners)),
			)
			results[parentName] = resolvedRouteDetails{
				httpRoute:        httpRoute,
				gatewayDetails:   resolvedGatewayData,
				matchedRef:       makeTargetOnlyParentRef(parentRef),
				matchedListeners: matchingListeners,
			}
		} else {
			m.logger.DebugContext(ctx, "Gateway resolved without section name",
				slog.String("parentName", parentName.String()),
			)
			results[parentName] = resolvedRouteDetails{
				httpRoute:        httpRoute,
				gatewayDetails:   resolvedGatewayData,
				matchedRef:       makeTargetOnlyParentRef(parentRef),
				matchedListeners: resolvedGatewayData.gateway.Spec.Listeners,
			}
		}
	}

	if len(results) == 0 {
		m.logger.InfoContext(ctx, "No relevant gateway found for HTTProute",
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

// TODO: The only reason to have this is to check that all backend refs are valid services.
// Need to investigate if we really need to do this.
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
	// backend set is created per rule with services as backends
	// for the future: services must have same port to make health check work
	// backend set name must be derived from the http route name + rule name (or index if name is empty)

	for i, rule := range params.httpRoute.Spec.Rules {
		bsName := backendSetName(params.httpRoute, rule, i)

		// TODO: Some check is required (on accept level) to check that refs within the same rule have same port
		// as well as liveliness probes. OCI load balancer does not support per backend HC
		// Also make sure there is at least one backend ref

		firstBackendRef := rule.BackendRefs[0]
		port := int32(*firstBackendRef.BackendRef.Port)

		err := m.ociLoadBalancerModel.reconcileBackendSet(ctx, reconcileBackendSetParams{
			loadBalancerID: params.config.Spec.LoadBalancerID,
			name:           bsName,

			// TODO: Consider using HTTP health check
			// Need some investigation to prove that it makes sense. We may potentially be
			// duplicating health checks. There should be pod level liveliness probes
			healthChecker: &loadbalancer.HealthCheckerDetails{
				Protocol: lo.ToPtr("TCP"),
				Port:     lo.ToPtr(int(port)),
			},
		})
		if err != nil {
			return fmt.Errorf("failed to reconcile backend set %s: %w", bsName, err)
		}
	}

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

	resolvedRefsCondition := meta.FindStatusCondition(
		parentStatus.Conditions,
		string(gatewayv1.RouteConditionResolvedRefs),
	)
	if resolvedRefsCondition == nil {
		return true, nil
	}

	if resolvedRefsCondition.Status == metav1.ConditionTrue &&
		resolvedRefsCondition.ObservedGeneration == details.httpRoute.Generation {
		return false, nil
	}

	return true, nil
}

func (m *httpRouteModelImpl) setProgrammed(
	ctx context.Context,
	params setProgrammedParams,
) error {
	httpRoute := params.httpRoute.DeepCopy()

	parentStatus, parentStatusIndex, found := lo.FindIndexOf(
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

	// Update the condition for the specific parent status
	meta.SetStatusCondition(&parentStatus.Conditions, metav1.Condition{
		Type:               string(gatewayv1.RouteConditionResolvedRefs),
		Status:             metav1.ConditionTrue,
		Reason:             string(gatewayv1.RouteReasonResolvedRefs),
		ObservedGeneration: httpRoute.Generation,
		LastTransitionTime: metav1.Now(),
		Message:            fmt.Sprintf("Route programmed by %s", params.gateway.Name),
	})

	httpRoute.Status.Parents[parentStatusIndex] = parentStatus

	m.logger.DebugContext(ctx, "Updating HTTProute status as Programmed (ResolvedRefs=True)",
		slog.String("route", httpRoute.Name),
		slog.String("gateway", params.gateway.Name),
	)
	if err := m.client.Status().Update(ctx, httpRoute); err != nil {
		return fmt.Errorf("failed to update programmed status for HTTProute %s: %w", httpRoute.Name, err)
	}

	return nil
}

// httpRouteModelDeps defines the dependencies required for the httpRouteModel.
type httpRouteModelDeps struct {
	dig.In

	K8sClient    k8sClient
	RootLogger   *slog.Logger
	GatewayModel gatewayModel
	OciLBModel   ociLoadBalancerModel
}

// newHTTPRouteModel creates a new instance of httpRouteModel.
func newHTTPRouteModel(deps httpRouteModelDeps) httpRouteModel {
	return &httpRouteModelImpl{
		client:               deps.K8sClient,
		logger:               deps.RootLogger.WithGroup("httproute-model"),
		gatewayModel:         deps.GatewayModel,
		ociLoadBalancerModel: deps.OciLBModel,
	}
}
