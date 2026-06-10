package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/types"
)

const tlsRouteBackendSetPolicy = "ROUND_ROBIN"
const tlsRouteLoadBalancerProtocol = "TCP"

type resolvedTLSRouteDetails struct {
	gatewayDetails  resolvedGatewayDetails
	tlsRoute        gatewayv1.TLSRoute
	matchedRef      gatewayv1.ParentReference
	matchedListener gatewayv1.Listener
}

type tlsRouteModel interface {
	resolveRequest(ctx context.Context, req reconcile.Request) ([]resolvedTLSRouteDetails, error)
	programRoute(ctx context.Context, details resolvedTLSRouteDetails) error
	deprovisionRoute(ctx context.Context, details resolvedTLSRouteDetails) error
	setProgrammed(ctx context.Context, details resolvedTLSRouteDetails) error
	setRejected(ctx context.Context, details resolvedTLSRouteDetails, statusErr tlsRouteStatusError) error
}

type tlsRouteStatusError struct {
	conditionType gatewayv1.RouteConditionType
	reason        gatewayv1.RouteConditionReason
	message       string
}

func (e tlsRouteStatusError) Error() string {
	return e.message
}

func newTLSRouteAcceptedStatusError(reason gatewayv1.RouteConditionReason, message string) tlsRouteStatusError {
	return tlsRouteStatusError{conditionType: gatewayv1.RouteConditionAccepted, reason: reason, message: message}
}

func newTLSRouteResolvedRefsStatusError(reason gatewayv1.RouteConditionReason, message string) tlsRouteStatusError {
	return tlsRouteStatusError{conditionType: gatewayv1.RouteConditionResolvedRefs, reason: reason, message: message}
}

type tlsRouteModelImpl struct {
	client                    k8sClient
	logger                    *slog.Logger
	networkLoadBalancerModel  networkLoadBalancerGatewayModel
	ociNetworkLoadBalancerAPI ociNetworkLoadBalancerClient
	ociLoadBalancerModel      ociLoadBalancerModel
	ociLoadBalancerAPI        ociLoadBalancerClient
	workRequestsWatcher       workRequestsWatcher
	nlbWorkRequestsWatcher    workRequestsWatcher
	operationLocks            *networkLoadBalancerOperationLocks
}

func tlsRouteKey(route gatewayv1.TLSRoute) string {
	return fmt.Sprintf("%s/%s", route.Namespace, route.Name)
}

func tlsRouteParentRefTarget(parentRef gatewayv1.ParentReference, routeNamespace string) apitypes.NamespacedName {
	return tcpParentRefTarget(parentRef, routeNamespace)
}

func tlsRouteMatchesListener(parentRef gatewayv1.ParentReference, listener gatewayv1.Listener) bool {
	if listener.Protocol != gatewayv1.TLSProtocolType {
		return false
	}
	if parentRef.SectionName != nil && *parentRef.SectionName != listener.Name {
		return false
	}
	if parentRef.Port != nil && *parentRef.Port != listener.Port {
		return false
	}
	return true
}

func tlsRouteMode(listener gatewayv1.Listener) (gatewayv1.TLSModeType, bool) {
	if listener.TLS == nil || listener.TLS.Mode == nil {
		return "", false
	}
	return *listener.TLS.Mode, true
}

func tlsRouteBackendSetName(route gatewayv1.TLSRoute, listener gatewayv1.Listener) string {
	return ociBackendSetNameFromService(corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: route.Namespace,
			Name:      fmt.Sprintf("%s-%s", route.Name, listener.Name),
		},
	})
}

func (m *tlsRouteModelImpl) resolveRequest(
	ctx context.Context,
	req reconcile.Request,
) ([]resolvedTLSRouteDetails, error) {
	route := &gatewayv1.TLSRoute{}
	results, err := resolveL4RouteRequest(ctx, resolveL4RouteRequestParams[resolvedTLSRouteDetails]{
		k8sClient: m.client,
		logger:    m.logger,
		req:       req,
		routeKind: "TLSRoute",
		route:     route,
		parentRefs: func() []gatewayv1.ParentReference {
			return route.Spec.ParentRefs
		},
		resolveParentRef: func(parentRef gatewayv1.ParentReference) ([]resolvedTLSRouteDetails, bool, error) {
			return m.resolveParentRef(ctx, *route, parentRef)
		},
		rejectNoMatchingListener: func(parentRef gatewayv1.ParentReference) error {
			return m.rejectNoMatchingListener(ctx, *route, parentRef)
		},
		finalizer: NetworkLoadBalancerTLSRouteProgrammedFinalizer,
		handleUnresolvedFinalizedRoute: func() error {
			return m.handleUnresolvedFinalizedRoute(ctx, *route)
		},
	})
	if err != nil || len(results) > 0 {
		return results, err
	}
	if controllerutil.ContainsFinalizer(route, LoadBalancerTLSRouteProgrammedFinalizer) {
		return nil, m.handleUnresolvedFinalizedRoute(ctx, *route)
	}
	return results, nil
}

func (m *tlsRouteModelImpl) resolveParentRef(
	ctx context.Context,
	route gatewayv1.TLSRoute,
	parentRef gatewayv1.ParentReference,
) ([]resolvedTLSRouteDetails, bool, error) {
	gatewayDetails, resolved, err := m.resolveParentGateway(ctx, route.Namespace, parentRef)
	if err != nil || !resolved {
		return nil, resolved, err
	}

	results := make([]resolvedTLSRouteDetails, 0)
	for _, listener := range gatewayDetails.gateway.Spec.Listeners {
		if !tlsRouteMatchesListener(parentRef, listener) {
			continue
		}
		results = append(results, resolvedTLSRouteDetails{
			tlsRoute:        route,
			gatewayDetails:  gatewayDetails,
			matchedRef:      makeTargetOnlyParentRef(parentRef),
			matchedListener: listener,
		})
	}
	return results, true, nil
}

func (m *tlsRouteModelImpl) resolveParentGateway(
	ctx context.Context,
	routeNamespace string,
	parentRef gatewayv1.ParentReference,
) (resolvedGatewayDetails, bool, error) {
	gatewayName := tlsRouteParentRefTarget(parentRef, routeNamespace)
	var gateway gatewayv1.Gateway
	if err := m.client.Get(ctx, gatewayName, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			return resolvedGatewayDetails{}, false, nil
		}
		return resolvedGatewayDetails{}, false, fmt.Errorf("failed to get Gateway %s: %w", gatewayName, err)
	}

	var gatewayClass gatewayv1.GatewayClass
	if err := m.client.Get(
		ctx,
		apitypes.NamespacedName{Name: string(gateway.Spec.GatewayClassName)},
		&gatewayClass,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return resolvedGatewayDetails{}, false, nil
		}
		return resolvedGatewayDetails{}, false, fmt.Errorf(
			"failed to get GatewayClass %s: %w",
			gateway.Spec.GatewayClassName,
			err,
		)
	}
	if !isSupportedControllerClassName(gatewayClass.Spec.ControllerName) {
		return resolvedGatewayDetails{}, false, nil
	}
	if gateway.Spec.Infrastructure == nil || gateway.Spec.Infrastructure.ParametersRef == nil {
		return resolvedGatewayDetails{}, false, nil
	}

	var config types.GatewayConfig
	if err := m.client.Get(ctx, apitypes.NamespacedName{
		Namespace: gateway.Namespace,
		Name:      gateway.Spec.Infrastructure.ParametersRef.Name,
	}, &config); err != nil {
		if apierrors.IsNotFound(err) {
			return resolvedGatewayDetails{}, false, nil
		}
		return resolvedGatewayDetails{}, false, fmt.Errorf(
			"failed to get GatewayConfig for Gateway %s: %w",
			gatewayName,
			err,
		)
	}

	return resolvedGatewayDetails{gateway: gateway, gatewayClass: gatewayClass, config: config}, true, nil
}

func (m *tlsRouteModelImpl) rejectNoMatchingListener(
	ctx context.Context,
	route gatewayv1.TLSRoute,
	parentRef gatewayv1.ParentReference,
) error {
	gatewayDetails, resolved, err := m.resolveParentGateway(ctx, route.Namespace, parentRef)
	if err != nil || !resolved {
		return err
	}
	return m.setRejected(ctx, resolvedTLSRouteDetails{
		tlsRoute:       route,
		gatewayDetails: gatewayDetails,
		matchedRef:     makeTargetOnlyParentRef(parentRef),
	}, newTLSRouteAcceptedStatusError(
		gatewayv1.RouteReasonNoMatchingParent,
		fmt.Sprintf("Gateway %s/%s has no TLS listener matching this parentRef",
			gatewayDetails.gateway.Namespace,
			gatewayDetails.gateway.Name,
		),
	))
}

func (m *tlsRouteModelImpl) handleUnresolvedFinalizedRoute(ctx context.Context, route gatewayv1.TLSRoute) error {
	if route.DeletionTimestamp != nil {
		return m.removeDeletingRouteFinalizers(ctx, route)
	}
	return m.deprovisionDetachedRoute(ctx, route)
}

func (m *tlsRouteModelImpl) removeDeletingRouteFinalizers(ctx context.Context, route gatewayv1.TLSRoute) error {
	routeToUpdate := route.DeepCopy()
	controllerutil.RemoveFinalizer(routeToUpdate, NetworkLoadBalancerTLSRouteProgrammedFinalizer)
	controllerutil.RemoveFinalizer(routeToUpdate, LoadBalancerTLSRouteProgrammedFinalizer)
	setAnnotatedBackendSetNames(routeToUpdate, NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation, nil)
	setAnnotatedBackendSetNames(routeToUpdate, LoadBalancerTLSRouteProgrammedBackendSetAnnotation, nil)
	if err := m.client.Update(ctx, routeToUpdate); err != nil {
		return fmt.Errorf("failed to remove finalizers from deleting TLSRoute %s/%s: %w",
			routeToUpdate.Namespace,
			routeToUpdate.Name,
			err,
		)
	}
	return nil
}

func (m *tlsRouteModelImpl) validateRoute(details resolvedTLSRouteDetails) error {
	if len(details.tlsRoute.Spec.Hostnames) == 0 {
		return newTLSRouteAcceptedStatusError(
			gatewayv1.RouteReasonNoMatchingListenerHostname,
			"TLSRoute must specify at least one hostname",
		)
	}
	mode, ok := tlsRouteMode(details.matchedListener)
	if !ok {
		return newTLSRouteAcceptedStatusError(
			gatewayv1.RouteReasonUnsupportedValue,
			fmt.Sprintf("listener %s must specify tls.mode", details.matchedListener.Name),
		)
	}

	switch details.gatewayDetails.gatewayClass.Spec.ControllerName {
	case ControllerClassName:
		if mode != gatewayv1.TLSModeTerminate {
			return newTLSRouteAcceptedStatusError(
				gatewayv1.RouteReasonUnsupportedValue,
				"OCI Load Balancer TLSRoute supports only Terminate mode",
			)
		}
	case NetworkLoadBalancerControllerClassName:
		if mode != gatewayv1.TLSModePassthrough {
			return newTLSRouteAcceptedStatusError(
				gatewayv1.RouteReasonUnsupportedValue,
				"OCI Network Load Balancer TLSRoute supports only Passthrough mode",
			)
		}
	default:
		return newTLSRouteAcceptedStatusError(
			gatewayv1.RouteReasonInvalidKind,
			"unsupported GatewayClass controller",
		)
	}
	return nil
}

func (m *tlsRouteModelImpl) endpointBackendsForRoute(
	ctx context.Context,
	route gatewayv1.TLSRoute,
) ([]networkloadbalancer.BackendDetails, error) {
	desired := make(map[string]networkloadbalancer.BackendDetails)
	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			backends, err := m.endpointBackendsForBackendRef(ctx, route, backendRef)
			if err != nil {
				return nil, err
			}
			for _, backend := range backends {
				mergeNetworkLoadBalancerBackend(desired, backend)
			}
		}
	}
	return lo.Values(desired), nil
}

func (m *tlsRouteModelImpl) endpointBackendsForBackendRef(
	ctx context.Context,
	route gatewayv1.TLSRoute,
	backendRef gatewayv1.BackendRef,
) ([]networkloadbalancer.BackendDetails, error) {
	weight := l4BackendRefWeight(backendRef)
	if weight == 0 {
		return nil, nil
	}
	fullName, servicePort, err := m.resolveBackendRefServicePort(ctx, route, backendRef)
	if err != nil {
		return nil, err
	}

	var endpointSlices discoveryv1.EndpointSliceList
	if listErr := m.client.List(ctx, &endpointSlices,
		client.MatchingLabels{discoveryv1.LabelServiceName: string(backendRef.BackendObjectReference.Name)},
		client.InNamespace(fullName.Namespace),
	); listErr != nil {
		return nil, fmt.Errorf("failed to list endpoint slices for backend %s: %w", fullName.String(), listErr)
	}
	return endpointBackendsForSlices(endpointSlices.Items, *servicePort, weight), nil
}

func (m *tlsRouteModelImpl) resolveBackendRefServicePort(
	ctx context.Context,
	route gatewayv1.TLSRoute,
	backendRef gatewayv1.BackendRef,
) (apitypes.NamespacedName, *corev1.ServicePort, error) {
	return resolveL4BackendRefServicePort(
		ctx,
		m.client,
		gatewayv1.Kind("TLSRoute"),
		route.Namespace,
		backendRef,
		tcpRouteBackendRefName(backendRef, route.Namespace),
		func(reason gatewayv1.RouteConditionReason, message string) error {
			return newTLSRouteResolvedRefsStatusError(reason, message)
		},
	)
}

func (m *tlsRouteModelImpl) ensureExclusiveListenerOwner(
	ctx context.Context,
	details resolvedTLSRouteDetails,
) error {
	currentRouteKey := tlsRouteKey(details.tlsRoute)
	matches, err := m.matchingRoutesForListener(ctx, details, "")
	return ensureExclusiveL4ListenerOwner(
		matches,
		currentRouteKey,
		details.matchedListener,
		"TLSRoute",
		func(reason gatewayv1.RouteConditionReason, message string) error {
			return newTLSRouteAcceptedStatusError(reason, message)
		},
		err,
	)
}

func (m *tlsRouteModelImpl) matchingRoutesForListener(
	ctx context.Context,
	details resolvedTLSRouteDetails,
	excludeRouteKey string,
) ([]l4RouteListenerMatch[gatewayv1.TLSRoute], error) {
	var routeList gatewayv1.TLSRouteList
	return listMatchingL4RoutesForListener(ctx, listMatchingL4RoutesForListenerParams[gatewayv1.TLSRoute]{
		k8sClient:       m.client,
		routeList:       &routeList,
		listError:       "failed to list TLSRoutes for listener ownership check",
		items:           func() []gatewayv1.TLSRoute { return routeList.Items },
		gatewayName:     client.ObjectKeyFromObject(&details.gatewayDetails.gateway),
		listener:        details.matchedListener,
		excludeRouteKey: excludeRouteKey,
		routeKey:        tlsRouteKey,
		routeNamespace:  func(route gatewayv1.TLSRoute) string { return route.Namespace },
		routeCreatedAt:  func(route gatewayv1.TLSRoute) metav1.Time { return route.CreationTimestamp },
		parentRefs:      func(route gatewayv1.TLSRoute) []gatewayv1.ParentReference { return route.Spec.ParentRefs },
		routeDeleted:    func(route gatewayv1.TLSRoute) bool { return route.DeletionTimestamp != nil },
		parentTarget:    tlsRouteParentRefTarget,
		matchesListener: tlsRouteMatchesListener,
	})
}

func (m *tlsRouteModelImpl) programRoute(ctx context.Context, details resolvedTLSRouteDetails) error {
	if err := m.validateRoute(details); err != nil {
		return err
	}
	allowed, err := l4ListenerAllowsRoute(
		ctx,
		m.client,
		details.gatewayDetails.gateway.Namespace,
		details.tlsRoute.Namespace,
		details.matchedListener,
		gatewayv1.Kind("TLSRoute"),
	)
	if err != nil {
		return err
	}
	if !allowed {
		return newTLSRouteAcceptedStatusError(
			gatewayv1.RouteReasonNotAllowedByListeners,
			fmt.Sprintf("listener %s does not allow TLSRoute %s/%s",
				details.matchedListener.Name,
				details.tlsRoute.Namespace,
				details.tlsRoute.Name,
			),
		)
	}
	if err = m.ensureExclusiveListenerOwner(ctx, details); err != nil {
		return err
	}

	if details.gatewayDetails.gatewayClass.Spec.ControllerName == ControllerClassName {
		return m.programLoadBalancerTerminateRoute(ctx, details)
	}
	return m.programNetworkLoadBalancerPassthroughRoute(ctx, details)
}

func (m *tlsRouteModelImpl) programNetworkLoadBalancerPassthroughRoute(
	ctx context.Context,
	details resolvedTLSRouteDetails,
) error {
	return programL4Route(ctx, newProgramL4RouteParams(newProgramL4RouteParamsInput{
		k8sClient:        m.client,
		routeKind:        "TLSRoute",
		route:            &details.tlsRoute,
		gatewayNamespace: details.gatewayDetails.gateway.Namespace,
		listener:         details.matchedListener,
		finalizer:        NetworkLoadBalancerTLSRouteProgrammedFinalizer,
		clearBackendSet:  func() error { return m.clearNLBBackendSet(ctx, details) },
		ensureOwner:      func() error { return nil },
		clearStale:       func() error { return nil },
		resolveBackends: func() ([]networkloadbalancer.BackendDetails, error) {
			return m.endpointBackendsForRoute(ctx, details.tlsRoute)
		},
		isResolvedRefsErr: func(err error) bool {
			var statusErr tlsRouteStatusError
			return errors.As(err, &statusErr) && statusErr.conditionType == gatewayv1.RouteConditionResolvedRefs
		},
		acceptedStatusError: func(reason gatewayv1.RouteConditionReason, message string) error {
			return newTLSRouteAcceptedStatusError(reason, message)
		},
		updateBackendSet: func(name string, backends []networkloadbalancer.BackendDetails) error {
			return m.updateNLBBackendSet(ctx, details, name, backends)
		},
	}))
}

func (m *tlsRouteModelImpl) updateNLBBackendSet(
	ctx context.Context,
	details resolvedTLSRouteDetails,
	backendSetName string,
	backends []networkloadbalancer.BackendDetails,
) error {
	healthCheckPort, err := m.routeHealthCheckPort(ctx, details.tlsRoute)
	if err != nil {
		return err
	}
	healthChecker := networkLoadBalancerHealthCheckerDetails(gatewayv1.TCPProtocolType, &healthCheckPort)
	lockID := networkLoadBalancerOperationLockID(details.gatewayDetails)
	return m.operationLocks.withLock(lockID, func() error {
		nlb, ensureErr := m.networkLoadBalancerModel.ensureNetworkLoadBalancer(ctx, &details.gatewayDetails)
		if ensureErr != nil {
			return ensureErr
		}
		if err = networkLoadBalancerBusyErrorFromState(nlb); err != nil {
			return err
		}
		if nlb.BackendSets != nil {
			currentBackendSet, ok := nlb.BackendSets[backendSetName]
			if ok &&
				tcpBackendsEqual(currentBackendSet.Backends, backends) &&
				networkLoadBalancerHealthCheckerMatches(currentBackendSet.HealthChecker, healthChecker) &&
				currentBackendSet.IsPreserveSource != nil &&
				!*currentBackendSet.IsPreserveSource {
				m.logger.DebugContext(ctx, "TLSRoute NLB backend set is already up-to-date",
					slog.String("tlsRoute", details.tlsRoute.Name),
					slog.String("backendSetName", backendSetName),
				)
				return nil
			}
		}

		return updateNetworkLoadBalancerBackendSet(
			ctx,
			m.ociNetworkLoadBalancerAPI,
			m.nlbWorkRequestsWatcher,
			nlb,
			backendSetName,
			"update",
			networkloadbalancer.UpdateBackendSetDetails{
				Policy:           new(string(networkloadbalancer.NetworkLoadBalancingPolicyFiveTuple)),
				HealthChecker:    &healthChecker,
				IsPreserveSource: new(false),
				Backends:         backends,
			},
		)
	})
}

func (m *tlsRouteModelImpl) clearNLBBackendSet(ctx context.Context, details resolvedTLSRouteDetails) error {
	backendSetName := networkLoadBalancerBackendSetName(details.matchedListener)
	healthChecker := networkLoadBalancerHealthCheckerDetails(
		gatewayv1.TCPProtocolType,
		new(int(details.matchedListener.Port)),
	)
	tcpModel := &tcpRouteModelImpl{
		client:                    m.client,
		logger:                    m.logger,
		networkLoadBalancerModel:  m.networkLoadBalancerModel,
		ociNetworkLoadBalancerAPI: m.ociNetworkLoadBalancerAPI,
		workRequestsWatcher:       m.nlbWorkRequestsWatcher,
		operationLocks:            m.operationLocks,
	}
	return tcpModel.clearBackendSetByName(
		ctx,
		details.gatewayDetails,
		tlsRouteKey(details.tlsRoute),
		backendSetName,
		&healthChecker,
	)
}

func (m *tlsRouteModelImpl) programLoadBalancerTerminateRoute(
	ctx context.Context,
	details resolvedTLSRouteDetails,
) error {
	loadBalancerID := details.gatewayDetails.config.Spec.LoadBalancerID
	lb, err := m.ociLoadBalancerAPI.GetLoadBalancer(ctx, loadbalancer.GetLoadBalancerRequest{
		LoadBalancerId: new(loadBalancerID),
	})
	if err != nil {
		return fmt.Errorf("failed to get OCI Load Balancer %s: %w", loadBalancerID, err)
	}

	backendSetName := tlsRouteBackendSetName(details.tlsRoute, details.matchedListener)
	backends, err := m.loadBalancerBackendsForRoute(ctx, details.tlsRoute)
	if err != nil {
		return err
	}
	healthCheckPort, err := m.routeHealthCheckPort(ctx, details.tlsRoute)
	if err != nil {
		return err
	}
	if err = m.reconcileLoadBalancerBackendSet(
		ctx,
		loadBalancerID,
		backendSetName,
		lb.BackendSets[backendSetName],
		backends,
		healthCheckPort,
	); err != nil {
		return err
	}

	certificates, err := m.ociLoadBalancerModel.reconcileListenersCertificates(
		ctx,
		reconcileListenersCertificatesParams{
			loadBalancerID:    loadBalancerID,
			gateway:           &details.gatewayDetails.gateway,
			knownCertificates: lb.Certificates,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to reconcile listener certificates: %w", err)
	}

	sslConfig, err := m.tlsListenerSSLConfig(details, certificates)
	if err != nil {
		return err
	}
	return m.reconcileLoadBalancerTLSListener(
		ctx,
		loadBalancerID,
		backendSetName,
		lb.Listeners[string(details.matchedListener.Name)],
		details.matchedListener,
		sslConfig,
	)
}

func (m *tlsRouteModelImpl) loadBalancerBackendsForRoute(
	ctx context.Context,
	route gatewayv1.TLSRoute,
) ([]loadbalancer.BackendDetails, error) {
	nlbBackends, err := m.endpointBackendsForRoute(ctx, route)
	if err != nil {
		return nil, err
	}
	return lo.Map(nlbBackends, func(backend networkloadbalancer.BackendDetails, _ int) loadbalancer.BackendDetails {
		return loadbalancer.BackendDetails{
			IpAddress: backend.IpAddress,
			Port:      backend.Port,
			Weight:    backend.Weight,
			Drain:     backend.IsDrain,
		}
	}), nil
}

func (m *tlsRouteModelImpl) routeHealthCheckPort(
	ctx context.Context,
	route gatewayv1.TLSRoute,
) (int, error) {
	backendRef, ok := tlsRouteFirstWeightedBackendRef(route)
	if !ok {
		return 0, newTLSRouteResolvedRefsStatusError(
			gatewayv1.RouteReasonInvalidKind,
			fmt.Sprintf("TLSRoute %s/%s has no backendRefs", route.Namespace, route.Name),
		)
	}
	return m.backendRefHealthCheckPort(ctx, route, backendRef)
}

func tlsRouteFirstWeightedBackendRef(route gatewayv1.TLSRoute) (gatewayv1.BackendRef, bool) {
	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			if l4BackendRefWeight(backendRef) != 0 {
				return backendRef, true
			}
		}
	}
	return gatewayv1.BackendRef{}, false
}

func (m *tlsRouteModelImpl) backendRefHealthCheckPort(
	ctx context.Context,
	route gatewayv1.TLSRoute,
	backendRef gatewayv1.BackendRef,
) (int, error) {
	fullName, servicePort, err := m.resolveBackendRefServicePort(ctx, route, backendRef)
	if err != nil {
		return 0, err
	}
	if servicePort.TargetPort.Type == 0 || servicePort.TargetPort.IntVal != 0 {
		port := servicePort.TargetPort.IntValue()
		if port == 0 {
			port = int(servicePort.Port)
		}
		return port, nil
	}

	var endpointSlices discoveryv1.EndpointSliceList
	if err = m.client.List(ctx, &endpointSlices,
		client.MatchingLabels{discoveryv1.LabelServiceName: string(backendRef.BackendObjectReference.Name)},
		client.InNamespace(fullName.Namespace),
	); err != nil {
		return 0, fmt.Errorf("failed to list endpoint slices for backend %s: %w", fullName.String(), err)
	}
	for _, slice := range endpointSlices.Items {
		if port, ok := l4EndpointPortForServicePort(*servicePort, slice); ok {
			return port, nil
		}
	}
	return int(servicePort.Port), nil
}

func loadBalancerBackendsEqual(current []loadbalancer.Backend, desired []loadbalancer.BackendDetails) bool {
	if len(current) != len(desired) {
		return false
	}
	currentMap := lo.SliceToMap(current, func(b loadbalancer.Backend) (string, loadbalancer.Backend) {
		return fmt.Sprintf("%s:%d", lo.FromPtr(b.IpAddress), lo.FromPtr(b.Port)), b
	})
	for _, backend := range desired {
		currentBackend, ok := currentMap[fmt.Sprintf("%s:%d", lo.FromPtr(backend.IpAddress), lo.FromPtr(backend.Port))]
		if !ok ||
			lo.FromPtr(currentBackend.Drain) != lo.FromPtr(backend.Drain) ||
			lo.FromPtr(currentBackend.Weight) != lo.FromPtr(backend.Weight) {
			return false
		}
	}
	return true
}

func (m *tlsRouteModelImpl) reconcileLoadBalancerBackendSet(
	ctx context.Context,
	loadBalancerID string,
	backendSetName string,
	existingBackendSet loadbalancer.BackendSet,
	backends []loadbalancer.BackendDetails,
	healthCheckPort int,
) error {
	healthChecker := loadBalancerBackendSetHealthChecker(healthCheckPort)
	desiredPolicy := tlsRouteBackendSetPolicy
	if existingBackendSet.Name != nil {
		if loadBalancerBackendSetMatches(existingBackendSet, desiredPolicy, healthChecker) &&
			loadBalancerBackendsEqual(existingBackendSet.Backends, backends) &&
			existingBackendSet.SslConfiguration == nil {
			return nil
		}
		updateRes, err := m.ociLoadBalancerAPI.UpdateBackendSet(ctx, loadbalancer.UpdateBackendSetRequest{
			LoadBalancerId: new(loadBalancerID),
			BackendSetName: new(backendSetName),
			UpdateBackendSetDetails: loadbalancer.UpdateBackendSetDetails{
				Policy:        new(desiredPolicy),
				Backends:      backends,
				HealthChecker: &healthChecker,
			},
		})
		if err != nil {
			return fmt.Errorf("failed to update TLSRoute backend set %s: %w", backendSetName, err)
		}
		if updateRes.OpcWorkRequestId == nil {
			return fmt.Errorf("failed to update TLSRoute backend set %s: missing work request id", backendSetName)
		}
		if err = m.workRequestsWatcher.WaitFor(ctx, *updateRes.OpcWorkRequestId); err != nil {
			return fmt.Errorf("failed to wait for TLSRoute backend set %s update: %w", backendSetName, err)
		}
		return nil
	}

	createRes, err := m.ociLoadBalancerAPI.CreateBackendSet(ctx, loadbalancer.CreateBackendSetRequest{
		LoadBalancerId: new(loadBalancerID),
		CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
			Name:          new(backendSetName),
			Policy:        new(desiredPolicy),
			HealthChecker: &healthChecker,
			Backends:      backends,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create TLSRoute backend set %s: %w", backendSetName, err)
	}
	if createRes.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to create TLSRoute backend set %s: missing work request id", backendSetName)
	}
	if err = m.workRequestsWatcher.WaitFor(ctx, *createRes.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed to wait for TLSRoute backend set %s creation: %w", backendSetName, err)
	}
	return nil
}

func (m *tlsRouteModelImpl) tlsListenerSSLConfig(
	details resolvedTLSRouteDetails,
	certificates reconcileListenersCertificatesResult,
) (*loadbalancer.SslConfigurationDetails, error) {
	listenerName := string(details.matchedListener.Name)
	if certificateID := certificates.certificateIDsByListener[listenerName]; certificateID != "" {
		return &loadbalancer.SslConfigurationDetails{CertificateIds: []string{certificateID}}, nil
	}
	listenerCertificates := certificates.certificatesByListener[listenerName]
	if len(listenerCertificates) == 0 {
		return nil, newTLSRouteAcceptedStatusError(
			gatewayv1.RouteReasonInvalidKind,
			fmt.Sprintf("listener %s requires certificateRefs or %s TLS option",
				listenerName,
				ListenerTLSOptionOCICertificateOCID,
			),
		)
	}
	return &loadbalancer.SslConfigurationDetails{CertificateName: listenerCertificates[0].CertificateName}, nil
}

func (m *tlsRouteModelImpl) reconcileLoadBalancerTLSListener(
	ctx context.Context,
	loadBalancerID string,
	backendSetName string,
	existingListener loadbalancer.Listener,
	listener gatewayv1.Listener,
	sslConfig *loadbalancer.SslConfigurationDetails,
) error {
	listenerName := string(listener.Name)
	updateDetails, hasExistingChanges := makeOciTLSListenerUpdateDetails(
		existingListener,
		listener,
		backendSetName,
		sslConfig,
	)
	if existingListener.Name != nil {
		if !hasExistingChanges {
			return nil
		}
		updateRes, err := m.ociLoadBalancerAPI.UpdateListener(ctx, loadbalancer.UpdateListenerRequest{
			LoadBalancerId:        new(loadBalancerID),
			ListenerName:          new(listenerName),
			UpdateListenerDetails: updateDetails,
		})
		if err != nil {
			return fmt.Errorf("failed to update TLSRoute listener %s: %w", listenerName, err)
		}
		if updateRes.OpcWorkRequestId == nil {
			return fmt.Errorf("failed to update TLSRoute listener %s: missing work request id", listenerName)
		}
		if err = m.workRequestsWatcher.WaitFor(ctx, *updateRes.OpcWorkRequestId); err != nil {
			return fmt.Errorf("failed to wait for TLSRoute listener %s update: %w", listenerName, err)
		}
		return nil
	}

	createRes, err := m.ociLoadBalancerAPI.CreateListener(ctx, loadbalancer.CreateListenerRequest{
		LoadBalancerId: new(loadBalancerID),
		CreateListenerDetails: loadbalancer.CreateListenerDetails{
			Name:                  new(listenerName),
			DefaultBackendSetName: new(backendSetName),
			Port:                  new(int(listener.Port)),
			Protocol:              new(tlsRouteLoadBalancerProtocol),
			SslConfiguration:      sslConfig,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create TLSRoute listener %s: %w", listenerName, err)
	}
	if createRes.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to create TLSRoute listener %s: missing work request id", listenerName)
	}
	if err = m.workRequestsWatcher.WaitFor(ctx, *createRes.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed to wait for TLSRoute listener %s creation: %w", listenerName, err)
	}
	return nil
}

func makeOciTLSListenerUpdateDetails(
	existingListener loadbalancer.Listener,
	listener gatewayv1.Listener,
	backendSetName string,
	sslConfig *loadbalancer.SslConfigurationDetails,
) (loadbalancer.UpdateListenerDetails, bool) {
	hasChanges := existingListener.Protocol == nil || *existingListener.Protocol != tlsRouteLoadBalancerProtocol
	if existingListener.Port == nil || *existingListener.Port != int(listener.Port) {
		hasChanges = true
	}
	if lo.FromPtr(existingListener.DefaultBackendSetName) != backendSetName {
		hasChanges = true
	}
	if lo.FromPtr(existingListener.RoutingPolicyName) != "" {
		hasChanges = true
	}
	existingCertName := ""
	existingCertIDs := normalizeCertificateIDs(nil)
	if existingListener.SslConfiguration != nil {
		existingCertName = lo.FromPtr(existingListener.SslConfiguration.CertificateName)
		existingCertIDs = normalizeCertificateIDs(existingListener.SslConfiguration.CertificateIds)
	}
	newCertName := ""
	newCertIDs := normalizeCertificateIDs(nil)
	if sslConfig != nil {
		newCertName = lo.FromPtr(sslConfig.CertificateName)
		newCertIDs = normalizeCertificateIDs(sslConfig.CertificateIds)
	}
	if existingCertName != newCertName || !lo.EveryBy(newCertIDs, func(certID string) bool {
		return lo.Contains(existingCertIDs, certID)
	}) || len(existingCertIDs) != len(newCertIDs) {
		hasChanges = true
	}
	if !hasChanges {
		return loadbalancer.UpdateListenerDetails{}, false
	}
	return loadbalancer.UpdateListenerDetails{
		Protocol:              new(tlsRouteLoadBalancerProtocol),
		Port:                  new(int(listener.Port)),
		DefaultBackendSetName: new(backendSetName),
		SslConfiguration:      sslConfig,
	}, true
}

func (m *tlsRouteModelImpl) deprovisionRoute(ctx context.Context, details resolvedTLSRouteDetails) error {
	if details.gatewayDetails.gatewayClass.Spec.ControllerName == ControllerClassName {
		return m.deprovisionLoadBalancerRoute(ctx, details)
	}
	return m.deprovisionNetworkLoadBalancerRoute(ctx, details)
}

func (m *tlsRouteModelImpl) deprovisionNetworkLoadBalancerRoute(
	ctx context.Context,
	details resolvedTLSRouteDetails,
) error {
	return deprovisionL4Route(ctx, deprovisionL4RouteParams[resolvedTLSRouteDetails]{
		k8sClient:          m.client,
		routeKind:          "TLSRoute",
		routeToUpdate:      details.tlsRoute.DeepCopy(),
		finalizer:          NetworkLoadBalancerTLSRouteProgrammedFinalizer,
		backendSetAnnotKey: NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation,
		nextRoute: func() (*resolvedTLSRouteDetails, error) {
			return m.nextEligibleRouteForListener(ctx, details)
		},
		programRoute: func(route resolvedTLSRouteDetails) error {
			return m.programRoute(ctx, route)
		},
		setProgrammed: func(route resolvedTLSRouteDetails) error {
			return m.setProgrammed(ctx, route)
		},
		routeObject: func(route resolvedTLSRouteDetails) client.Object {
			return &route.tlsRoute
		},
		clearBackendSet: func() error { return m.clearNLBBackendSet(ctx, details) },
	})
}

func (m *tlsRouteModelImpl) nextEligibleRouteForListener(
	ctx context.Context,
	details resolvedTLSRouteDetails,
) (*resolvedTLSRouteDetails, error) {
	currentRouteKey := tlsRouteKey(details.tlsRoute)
	matches, err := m.matchingRoutesForListener(ctx, details, currentRouteKey)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		var missing *resolvedTLSRouteDetails
		return missing, nil
	}
	return &resolvedTLSRouteDetails{
		gatewayDetails:  details.gatewayDetails,
		tlsRoute:        matches[0].route,
		matchedRef:      makeTargetOnlyParentRef(matches[0].matchedRef),
		matchedListener: details.matchedListener,
	}, nil
}

func (m *tlsRouteModelImpl) deprovisionLoadBalancerRoute(ctx context.Context, details resolvedTLSRouteDetails) error {
	nextRoute, err := m.nextEligibleRouteForListener(ctx, details)
	if err != nil {
		return err
	}
	if nextRoute != nil {
		if err = m.programRoute(ctx, *nextRoute); err != nil {
			return fmt.Errorf("failed to program next TLSRoute %s/%s: %w",
				nextRoute.tlsRoute.Namespace,
				nextRoute.tlsRoute.Name,
				err,
			)
		}
		if err = m.setProgrammed(ctx, *nextRoute); err != nil {
			return fmt.Errorf("failed to set next TLSRoute %s/%s programmed status: %w",
				nextRoute.tlsRoute.Namespace,
				nextRoute.tlsRoute.Name,
				err,
			)
		}
	} else if err = m.deleteLoadBalancerRouteResources(ctx, details); err != nil {
		return err
	}

	routeToUpdate := details.tlsRoute.DeepCopy()
	controllerutil.RemoveFinalizer(routeToUpdate, LoadBalancerTLSRouteProgrammedFinalizer)
	setAnnotatedBackendSetNames(routeToUpdate, LoadBalancerTLSRouteProgrammedBackendSetAnnotation, nil)
	if updateErr := m.client.Update(ctx, routeToUpdate); updateErr != nil {
		return fmt.Errorf("failed to remove ALB TLSRoute finalizer from %s/%s: %w",
			routeToUpdate.Namespace,
			routeToUpdate.Name,
			updateErr,
		)
	}
	return nil
}

func (m *tlsRouteModelImpl) deleteLoadBalancerRouteResources(
	ctx context.Context,
	details resolvedTLSRouteDetails,
) error {
	loadBalancerID := details.gatewayDetails.config.Spec.LoadBalancerID
	listenerName := string(details.matchedListener.Name)
	deleteListenerRes, err := m.ociLoadBalancerAPI.DeleteListener(ctx, loadbalancer.DeleteListenerRequest{
		LoadBalancerId: new(loadBalancerID),
		ListenerName:   new(listenerName),
	})
	if err != nil {
		serviceErr, ok := common.IsServiceError(err)
		if !ok || serviceErr.GetHTTPStatusCode() != http.StatusNotFound {
			return fmt.Errorf("failed to delete TLSRoute listener %s: %w", listenerName, err)
		}
	} else {
		if deleteListenerRes.OpcWorkRequestId == nil {
			return fmt.Errorf("failed to delete TLSRoute listener %s: missing work request id", listenerName)
		}
		if err = m.workRequestsWatcher.WaitFor(ctx, *deleteListenerRes.OpcWorkRequestId); err != nil {
			return fmt.Errorf("failed to wait for TLSRoute listener %s deletion: %w", listenerName, err)
		}
	}

	backendSetName := tlsRouteBackendSetName(details.tlsRoute, details.matchedListener)
	deleteBackendSetRes, err := m.ociLoadBalancerAPI.DeleteBackendSet(ctx, loadbalancer.DeleteBackendSetRequest{
		LoadBalancerId: new(loadBalancerID),
		BackendSetName: new(backendSetName),
	})
	if err != nil {
		serviceErr, ok := common.IsServiceError(err)
		if !ok || serviceErr.GetHTTPStatusCode() != http.StatusNotFound {
			return fmt.Errorf("failed to delete TLSRoute backend set %s: %w", backendSetName, err)
		}
		return nil
	}
	if deleteBackendSetRes.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to delete TLSRoute backend set %s: missing work request id", backendSetName)
	}
	if err = m.workRequestsWatcher.WaitFor(ctx, *deleteBackendSetRes.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed to wait for TLSRoute backend set %s deletion: %w", backendSetName, err)
	}
	return nil
}

func (m *tlsRouteModelImpl) deprovisionDetachedRoute(ctx context.Context, route gatewayv1.TLSRoute) error {
	return m.removeDeletingRouteFinalizers(ctx, route)
}

func (m *tlsRouteModelImpl) updateParentStatus(
	ctx context.Context,
	details resolvedTLSRouteDetails,
	conditions []metav1.Condition,
) error {
	details.tlsRoute.Status.Parents = mergeTLSRouteParentStatus(
		details.tlsRoute.Status.Parents,
		details.matchedRef,
		details.gatewayDetails.gatewayClass.Spec.ControllerName,
		conditions,
	)
	if err := m.client.Status().Update(ctx, &details.tlsRoute); err != nil {
		return fmt.Errorf("failed to update TLSRoute %s status: %w", details.tlsRoute.Name, err)
	}
	return nil
}

func mergeTLSRouteParentStatus(
	parents []gatewayv1.RouteParentStatus,
	parentRef gatewayv1.ParentReference,
	controllerName gatewayv1.GatewayController,
	conditions []metav1.Condition,
) []gatewayv1.RouteParentStatus {
	parentStatus := gatewayv1.RouteParentStatus{
		ParentRef:      parentRef,
		ControllerName: controllerName,
		Conditions:     conditions,
	}
	result := make([]gatewayv1.RouteParentStatus, 0, len(parents))
	found := false
	for _, status := range parents {
		if status.ControllerName != parentStatus.ControllerName ||
			!parentRefSameTarget(status.ParentRef, parentStatus.ParentRef) {
			result = append(result, status)
			continue
		}
		updatedParent := status
		updatedParent.ParentRef = parentStatus.ParentRef
		updatedParent.ControllerName = parentStatus.ControllerName
		for _, condition := range parentStatus.Conditions {
			meta.SetStatusCondition(&updatedParent.Conditions, condition)
		}
		result = append(result, updatedParent)
		found = true
	}
	if found {
		return result
	}
	return append(parents, parentStatus)
}

func (m *tlsRouteModelImpl) setProgrammed(ctx context.Context, details resolvedTLSRouteDetails) error {
	routeToUpdate := details.tlsRoute.DeepCopy()
	finalizer := tlsRouteFinalizerForDetails(details)
	annotationKey := lo.Ternary(
		details.gatewayDetails.gatewayClass.Spec.ControllerName == ControllerClassName,
		LoadBalancerTLSRouteProgrammedBackendSetAnnotation,
		NetworkLoadBalancerTLSRouteProgrammedBackendSetsAnnotation,
	)
	desiredBackendSets := map[string]struct{}{}
	if details.gatewayDetails.gatewayClass.Spec.ControllerName == ControllerClassName {
		desiredBackendSets[tlsRouteBackendSetName(details.tlsRoute, details.matchedListener)] = struct{}{}
	} else {
		desiredBackendSets[networkLoadBalancerBackendSetName(details.matchedListener)] = struct{}{}
	}
	if err := setL4RouteProgrammed(ctx, setL4RouteProgrammedParams{
		k8sClient:          m.client,
		routeKind:          "TLSRoute",
		routeToUpdate:      routeToUpdate,
		finalizer:          finalizer,
		backendSetAnnotKey: annotationKey,
		desiredBackendSets: desiredBackendSets,
		updateParentStatus: func(conditions []metav1.Condition) error {
			return m.updateParentStatus(ctx, resolvedTLSRouteDetails{
				gatewayDetails:  details.gatewayDetails,
				tlsRoute:        *routeToUpdate,
				matchedRef:      details.matchedRef,
				matchedListener: details.matchedListener,
			}, conditions)
		},
	}); err != nil {
		return err
	}
	return nil
}

func (m *tlsRouteModelImpl) setRejected(
	ctx context.Context,
	details resolvedTLSRouteDetails,
	statusErr tlsRouteStatusError,
) error {
	return m.updateParentStatus(ctx, details, []metav1.Condition{
		{
			Type:               string(statusErr.conditionType),
			Status:             metav1.ConditionFalse,
			Reason:             string(statusErr.reason),
			Message:            statusErr.message,
			ObservedGeneration: details.tlsRoute.Generation,
			LastTransitionTime: metav1.Now(),
		},
	})
}

type tlsRouteModelDeps struct {
	dig.In

	RootLogger                *slog.Logger
	K8sClient                 k8sClient
	NetworkLoadBalancerModel  networkLoadBalancerGatewayModel
	OciNetworkLoadBalancerAPI ociNetworkLoadBalancerClient
	OciLoadBalancerModel      ociLoadBalancerModel
	OciLoadBalancerAPI        ociLoadBalancerClient
	WorkRequestsWatcher       workRequestsWatcher
	NLBWorkRequestsWatcher    workRequestsWatcher `name:"networkLoadBalancerWorkRequestsWatcher"`
	OperationLocks            *networkLoadBalancerOperationLocks
}

func newTLSRouteModel(deps tlsRouteModelDeps) *tlsRouteModelImpl {
	operationLocks := deps.OperationLocks
	if operationLocks == nil {
		operationLocks = newNetworkLoadBalancerOperationLocks()
	}
	watcher := deps.WorkRequestsWatcher
	if watcher == nil {
		watcher = noopWorkRequestsWatcher{}
	}
	nlbWatcher := deps.NLBWorkRequestsWatcher
	if nlbWatcher == nil {
		nlbWatcher = noopWorkRequestsWatcher{}
	}
	return &tlsRouteModelImpl{
		client:                    deps.K8sClient,
		logger:                    deps.RootLogger.WithGroup("tlsroute-model"),
		networkLoadBalancerModel:  deps.NetworkLoadBalancerModel,
		ociNetworkLoadBalancerAPI: deps.OciNetworkLoadBalancerAPI,
		ociLoadBalancerModel:      deps.OciLoadBalancerModel,
		ociLoadBalancerAPI:        deps.OciLoadBalancerAPI,
		workRequestsWatcher:       watcher,
		nlbWorkRequestsWatcher:    nlbWatcher,
		operationLocks:            operationLocks,
	}
}
