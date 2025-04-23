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
	gatewayDetails acceptedGatewayDetails
	matchedRef     gatewayv1.ParentReference
	httpRoute      gatewayv1.HTTPRoute
}

type resolveBackendRefsParams struct {
	httpRoute gatewayv1.HTTPRoute
}

type programRouteParams struct {
	gateway             gatewayv1.Gateway
	config              types.GatewayConfig
	httpRoute           gatewayv1.HTTPRoute
	resolvedBackendRefs map[string]v1.Service
}

// httpRouteModel defines the interface for managing HTTPRoute resources.
type httpRouteModel interface {
	// resolveRequest resolves the parent details for a given HTTPRoute.
	// It returns true if the request is relevant for this controller and
	// the parent has been resolved.
	resolveRequest(
		ctx context.Context,
		req reconcile.Request,
		receiver *resolvedRouteDetails,
	) (bool, error)

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

	// programRoute programs a given HTTPRoute.
	programRoute(
		ctx context.Context,
		params programRouteParams,
	) error
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
	client       k8sClient
	logger       *slog.Logger
	gatewayModel gatewayModel
	ociLoadBalancerModel
}

func (m *httpRouteModelImpl) resolveRequest(
	ctx context.Context,
	req reconcile.Request,
	receiver *resolvedRouteDetails,
) (bool, error) {
	var httpRoute gatewayv1.HTTPRoute
	if err := m.client.Get(ctx, req.NamespacedName, &httpRoute); err != nil {
		if apierrors.IsNotFound(err) {
			m.logger.DebugContext(ctx, "HTTProute not found",
				slog.String("route", req.NamespacedName.String()),
			)
			return false, nil
		}
		return false, err
	}

	var resolvedGatewayData acceptedGatewayDetails
	var matchedRef gatewayv1.ParentReference
	gatewayAccepted := false
	for _, parentRef := range httpRoute.Spec.ParentRefs {
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
		accepted, err := m.gatewayModel.acceptReconcileRequest(ctx, reconcile.Request{
			NamespacedName: parentName,
		}, &resolvedGatewayData)
		if err != nil {
			return false, fmt.Errorf("failed to accept reconcile request for gateway %s: %w", parentRef.Name, err)
		}
		if accepted {
			gatewayAccepted = true
			matchedRef = parentRef
			break
		}
	}

	if !gatewayAccepted {
		m.logger.InfoContext(ctx, "No relevant gateway found for HTTProute",
			slog.String("route", req.NamespacedName.String()),
			slog.Int("triedParentRefs", len(httpRoute.Spec.ParentRefs)),
		)
		return false, nil
	}

	m.logger.InfoContext(ctx, "Resolved relevant HTTProute parent",
		slog.String("route", req.NamespacedName.String()),
		slog.String("gateway", resolvedGatewayData.gateway.Name),
	)

	receiver.httpRoute = httpRoute
	receiver.matchedRef = matchedRef
	receiver.gatewayDetails = resolvedGatewayData

	return true, nil
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
			return s.ControllerName == routeDetails.gatewayDetails.gatewayClass.Spec.ControllerName
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
			ParentRef:      routeDetails.matchedRef,
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
		ruleName := lo.TernaryF(
			rule.Name != nil,
			func() gatewayv1.SectionName { return *rule.Name },
			func() gatewayv1.SectionName { return gatewayv1.SectionName("rt-" + strconv.Itoa(i)) },
		)
		bsName := fmt.Sprintf("%s-%s", params.httpRoute.Name, ruleName)

		// TODO: Some check is required (on accept level) to check that refs within the same rule have same port
		// as well as livliness probes. OCI load balancer does not support per backend HC
		// Also make sure there is at least one backend ref

		firstBackendRef := rule.BackendRefs[0]
		port := int32(*firstBackendRef.BackendRef.Port)

		_, err := m.ociLoadBalancerModel.reconcileBackendSet(ctx, reconcileBackendSetParams{
			loadBalancerID: params.config.Spec.LoadBalancerID,
			name:           bsName,

			// TODO: Consider using HTTP health check
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
		logger:               deps.RootLogger.With("component", "httproute-model"),
		gatewayModel:         deps.GatewayModel,
		ociLoadBalancerModel: deps.OciLBModel,
	}
}
