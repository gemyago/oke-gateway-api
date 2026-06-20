package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

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
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"github.com/gemyago/oke-gateway-api/internal/types"
)

type resolvedTCPRouteDetails struct {
	gatewayDetails  resolvedGatewayDetails
	tcpRoute        gatewayv1alpha2.TCPRoute
	matchedRef      gatewayv1.ParentReference
	matchedListener gatewayv1.Listener
}

type tcpRouteModel interface {
	resolveRequest(ctx context.Context, req reconcile.Request) ([]resolvedTCPRouteDetails, error)
	programRoute(ctx context.Context, details resolvedTCPRouteDetails) error
	deprovisionRoute(ctx context.Context, details resolvedTCPRouteDetails) error
	setProgrammed(ctx context.Context, details resolvedTCPRouteDetails) error
	setRejected(ctx context.Context, details resolvedTCPRouteDetails, statusErr tcpRouteStatusError) error
}

type tcpRouteStatusError struct {
	conditionType gatewayv1.RouteConditionType
	reason        gatewayv1.RouteConditionReason
	message       string
}

func (e tcpRouteStatusError) Error() string {
	return e.message
}

func newTCPRouteAcceptedStatusError(reason gatewayv1.RouteConditionReason, message string) tcpRouteStatusError {
	return tcpRouteStatusError{
		conditionType: gatewayv1.RouteConditionAccepted,
		reason:        reason,
		message:       message,
	}
}

func newTCPRouteResolvedRefsStatusError(reason gatewayv1.RouteConditionReason, message string) tcpRouteStatusError {
	return tcpRouteStatusError{
		conditionType: gatewayv1.RouteConditionResolvedRefs,
		reason:        reason,
		message:       message,
	}
}

type tcpRouteModelImpl struct {
	client                    k8sClient
	logger                    *slog.Logger
	networkLoadBalancerModel  networkLoadBalancerGatewayModel
	ociNetworkLoadBalancerAPI ociNetworkLoadBalancerClient
	workRequestsWatcher       workRequestsWatcher
	operationLocks            *networkLoadBalancerOperationLocks
}

func tcpRouteBackendRefName(backendRef gatewayv1.BackendRef, defaultNamespace string) apitypes.NamespacedName {
	return apitypes.NamespacedName{
		Name: string(backendRef.BackendObjectReference.Name),
		Namespace: lo.IfF(
			backendRef.BackendObjectReference.Namespace != nil,
			func() string { return string(*backendRef.BackendObjectReference.Namespace) },
		).Else(defaultNamespace),
	}
}

func tcpParentRefTarget(parentRef gatewayv1.ParentReference, routeNamespace string) apitypes.NamespacedName {
	namespace := routeNamespace
	if parentRef.Namespace != nil {
		namespace = string(*parentRef.Namespace)
	}
	return apitypes.NamespacedName{
		Namespace: namespace,
		Name:      string(parentRef.Name),
	}
}

func tcpRouteMatchesListener(parentRef gatewayv1.ParentReference, listener gatewayv1.Listener) bool {
	if listener.Protocol != gatewayv1.TCPProtocolType {
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

func tcpRouteKey(route gatewayv1alpha2.TCPRoute) string {
	return fmt.Sprintf("%s/%s", route.Namespace, route.Name)
}

func desiredTCPRouteBackendSetNames(details resolvedTCPRouteDetails) map[string]struct{} {
	desired := make(map[string]struct{})
	gatewayName := apitypes.NamespacedName{
		Namespace: details.gatewayDetails.gateway.Namespace,
		Name:      details.gatewayDetails.gateway.Name,
	}
	for _, parentRef := range details.tcpRoute.Spec.ParentRefs {
		if !parentRefTargetsGateway(parentRef) ||
			tcpParentRefTarget(parentRef, details.tcpRoute.Namespace) != gatewayName {
			continue
		}
		for _, listener := range details.gatewayDetails.gateway.Spec.Listeners {
			if tcpRouteMatchesListener(parentRef, listener) {
				desired[networkLoadBalancerBackendSetName(listener)] = struct{}{}
			}
		}
	}
	return desired
}

func (m *tcpRouteModelImpl) resolveRequest(
	ctx context.Context,
	req reconcile.Request,
) ([]resolvedTCPRouteDetails, error) {
	route := &gatewayv1alpha2.TCPRoute{}
	return resolveL4RouteRequest(ctx, resolveL4RouteRequestParams[resolvedTCPRouteDetails]{
		k8sClient: m.client,
		logger:    m.logger,
		req:       req,
		routeKind: "TCPRoute",
		route:     route,
		parentRefs: func() []gatewayv1.ParentReference {
			return route.Spec.ParentRefs
		},
		resolveParentRef: func(parentRef gatewayv1.ParentReference) ([]resolvedTCPRouteDetails, bool, error) {
			return m.resolveParentRef(ctx, *route, parentRef)
		},
		rejectNoMatchingListener: func(parentRef gatewayv1.ParentReference) error {
			return m.rejectNoMatchingListener(ctx, *route, parentRef)
		},
		finalizer: NetworkLoadBalancerTCPRouteProgrammedFinalizer,
		handleUnresolvedFinalizedRoute: func() error {
			return m.handleUnresolvedFinalizedRoute(ctx, *route)
		},
	})
}

type resolveL4RouteRequestParams[D any] struct {
	k8sClient                      k8sClient
	logger                         *slog.Logger
	req                            reconcile.Request
	routeKind                      string
	route                          client.Object
	parentRefs                     func() []gatewayv1.ParentReference
	resolveParentRef               func(gatewayv1.ParentReference) ([]D, bool, error)
	rejectNoMatchingListener       func(gatewayv1.ParentReference) error
	finalizer                      string
	handleUnresolvedFinalizedRoute func() error
}

func resolveL4RouteRequest[D any](
	ctx context.Context,
	params resolveL4RouteRequestParams[D],
) ([]D, error) {
	if err := params.k8sClient.Get(ctx, params.req.NamespacedName, params.route); err != nil {
		if apierrors.IsNotFound(err) {
			params.logger.InfoContext(ctx, fmt.Sprintf("%s %s not found", params.routeKind, params.req.NamespacedName))
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get %s %s: %w", params.routeKind, params.req.NamespacedName, err)
	}

	var results []D
	for _, parentRef := range params.parentRefs() {
		if !parentRefTargetsGateway(parentRef) {
			continue
		}
		parentResults, resolved, err := params.resolveParentRef(parentRef)
		if err != nil {
			return nil, err
		}
		if !resolved {
			continue
		}
		if len(parentResults) == 0 && params.route.GetDeletionTimestamp() == nil {
			if err = params.rejectNoMatchingListener(parentRef); err != nil {
				return nil, err
			}
		}
		results = append(results, parentResults...)
	}

	if len(results) == 0 && controllerutil.ContainsFinalizer(params.route, params.finalizer) {
		if err := params.handleUnresolvedFinalizedRoute(); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (m *tcpRouteModelImpl) resolveParentRef(
	ctx context.Context,
	route gatewayv1alpha2.TCPRoute,
	parentRef gatewayv1.ParentReference,
) ([]resolvedTCPRouteDetails, bool, error) {
	gatewayDetails, resolved, err := m.resolveParentGateway(ctx, route.Namespace, parentRef)
	if err != nil || !resolved {
		return nil, resolved, err
	}

	results := make([]resolvedTCPRouteDetails, 0)
	for _, listener := range gatewayDetails.gateway.Spec.Listeners {
		if !tcpRouteMatchesListener(parentRef, listener) {
			continue
		}
		results = append(results, resolvedTCPRouteDetails{
			tcpRoute:        route,
			gatewayDetails:  gatewayDetails,
			matchedRef:      makeTargetOnlyParentRef(parentRef),
			matchedListener: listener,
		})
	}
	return results, true, nil
}

func (m *tcpRouteModelImpl) resolveParentGateway(
	ctx context.Context,
	routeNamespace string,
	parentRef gatewayv1.ParentReference,
) (resolvedGatewayDetails, bool, error) {
	return resolveL4ParentGateway(ctx, m.client, tcpParentRefTarget(parentRef, routeNamespace))
}

func resolveL4ParentGateway(
	ctx context.Context,
	k8sClient k8sClient,
	gatewayName apitypes.NamespacedName,
) (resolvedGatewayDetails, bool, error) {
	var gateway gatewayv1.Gateway
	if err := k8sClient.Get(ctx, gatewayName, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			return resolvedGatewayDetails{}, false, nil
		}
		return resolvedGatewayDetails{}, false, fmt.Errorf("failed to get Gateway %s: %w", gatewayName, err)
	}

	var gatewayClass gatewayv1.GatewayClass
	if err := k8sClient.Get(
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
	if gatewayClass.Spec.ControllerName != gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName) {
		return resolvedGatewayDetails{}, false, nil
	}
	if gateway.Spec.Infrastructure == nil || gateway.Spec.Infrastructure.ParametersRef == nil {
		return resolvedGatewayDetails{}, false, nil
	}

	var config types.GatewayConfig
	if err := k8sClient.Get(ctx, apitypes.NamespacedName{
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

func (m *tcpRouteModelImpl) rejectNoMatchingListener(
	ctx context.Context,
	route gatewayv1alpha2.TCPRoute,
	parentRef gatewayv1.ParentReference,
) error {
	gatewayDetails, resolved, err := m.resolveParentGateway(ctx, route.Namespace, parentRef)
	if err != nil || !resolved {
		return err
	}
	return m.setRejected(ctx, resolvedTCPRouteDetails{
		tcpRoute:       route,
		gatewayDetails: gatewayDetails,
		matchedRef:     makeTargetOnlyParentRef(parentRef),
	}, newTCPRouteAcceptedStatusError(
		gatewayv1.RouteReasonNoMatchingParent,
		fmt.Sprintf(
			"Gateway %s/%s has no TCP listener matching this parentRef",
			gatewayDetails.gateway.Namespace,
			gatewayDetails.gateway.Name,
		),
	))
}

func (m *tcpRouteModelImpl) handleUnresolvedFinalizedRoute(
	ctx context.Context,
	route gatewayv1alpha2.TCPRoute,
) error {
	if route.DeletionTimestamp != nil {
		return m.removeDeletingRouteFinalizer(ctx, route)
	}
	return m.deprovisionDetachedRoute(ctx, route)
}

func (m *tcpRouteModelImpl) removeDeletingRouteFinalizer(
	ctx context.Context,
	route gatewayv1alpha2.TCPRoute,
) error {
	routeToUpdate := route.DeepCopy()
	controllerutil.RemoveFinalizer(routeToUpdate, NetworkLoadBalancerTCPRouteProgrammedFinalizer)
	setAnnotatedBackendSetNames(routeToUpdate, NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation, nil)
	if err := m.client.Update(ctx, routeToUpdate); err != nil {
		return fmt.Errorf("failed to remove finalizer from deleting TCPRoute %s/%s: %w",
			routeToUpdate.Namespace,
			routeToUpdate.Name,
			err,
		)
	}
	return nil
}

func (m *tcpRouteModelImpl) endpointBackendsForRoute(
	ctx context.Context,
	route gatewayv1alpha2.TCPRoute,
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

func (m *tcpRouteModelImpl) endpointBackendsForBackendRef(
	ctx context.Context,
	route gatewayv1alpha2.TCPRoute,
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

func (m *tcpRouteModelImpl) resolveBackendRefServicePort(
	ctx context.Context,
	route gatewayv1alpha2.TCPRoute,
	backendRef gatewayv1.BackendRef,
) (apitypes.NamespacedName, *corev1.ServicePort, error) {
	return resolveL4BackendRefServicePort(
		ctx,
		m.client,
		gatewayv1.Kind("TCPRoute"),
		route.Namespace,
		backendRef,
		tcpRouteBackendRefName(backendRef, route.Namespace),
		func(reason gatewayv1.RouteConditionReason, message string) error {
			return newTCPRouteResolvedRefsStatusError(reason, message)
		},
	)
}

func resolveL4BackendRefServicePort(
	ctx context.Context,
	k8sClient k8sClient,
	routeKind gatewayv1.Kind,
	routeNamespace string,
	backendRef gatewayv1.BackendRef,
	fullName apitypes.NamespacedName,
	statusErr func(gatewayv1.RouteConditionReason, string) error,
) (apitypes.NamespacedName, *corev1.ServicePort, error) {
	if err := l4ValidateServiceBackendRef(backendRef); err != nil {
		return apitypes.NamespacedName{}, nil, statusErr(gatewayv1.RouteReasonInvalidKind, err.Error())
	}
	if backendRef.BackendObjectReference.Port == nil {
		return apitypes.NamespacedName{}, nil, statusErr(
			gatewayv1.RouteReasonInvalidKind,
			fmt.Sprintf("backendRef %s is missing port", backendRef.BackendObjectReference.Name),
		)
	}

	allowed, err := referenceGrantAllowsServiceBackend(ctx, k8sClient, routeKind, routeNamespace, fullName)
	if err != nil {
		return apitypes.NamespacedName{}, nil, err
	}
	if !allowed {
		return apitypes.NamespacedName{}, nil, statusErr(
			gatewayv1.RouteReasonRefNotPermitted,
			fmt.Sprintf("backendRef %s/%s is not permitted by a ReferenceGrant", fullName.Namespace, fullName.Name),
		)
	}

	var service corev1.Service
	if getErr := k8sClient.Get(ctx, fullName, &service); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			return apitypes.NamespacedName{}, nil, statusErr(
				gatewayv1.RouteReasonBackendNotFound,
				fmt.Sprintf("backendRef service %s not found", fullName.String()),
			)
		}
		return apitypes.NamespacedName{}, nil, fmt.Errorf("failed to get service %s: %w", fullName.String(), getErr)
	}

	servicePort, err := l4ServicePortForBackendRef(service, backendRef)
	if err != nil {
		return apitypes.NamespacedName{}, nil, statusErr(
			gatewayv1.RouteReasonInvalidKind,
			err.Error(),
		)
	}
	return fullName, servicePort, nil
}

func endpointBackendsForSlices(
	endpointSlices []discoveryv1.EndpointSlice,
	servicePort corev1.ServicePort,
	weight int,
) []networkloadbalancer.BackendDetails {
	backends := make([]networkloadbalancer.BackendDetails, 0)
	for _, slice := range endpointSlices {
		port, ok := l4EndpointPortForServicePort(servicePort, slice)
		if !ok {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
				continue
			}
			if len(endpoint.Addresses) == 0 {
				continue
			}
			isDraining := endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating
			backends = append(backends, networkloadbalancer.BackendDetails{
				IpAddress: new(endpoint.Addresses[0]),
				Port:      new(port),
				IsDrain:   new(isDraining),
				Weight:    new(weight),
			})
		}
	}
	return backends
}

func mergeNetworkLoadBalancerBackend(
	desired map[string]networkloadbalancer.BackendDetails,
	backend networkloadbalancer.BackendDetails,
) {
	key := fmt.Sprintf("%s:%d", lo.FromPtr(backend.IpAddress), lo.FromPtr(backend.Port))
	if existing, found := desired[key]; found {
		backend.Weight = new(lo.FromPtr(existing.Weight) + lo.FromPtr(backend.Weight))
		backend.IsDrain = new(lo.FromPtr(existing.IsDrain) && lo.FromPtr(backend.IsDrain))
	}
	desired[key] = backend
}

func tcpBackendsEqual(current []networkloadbalancer.Backend, desired []networkloadbalancer.BackendDetails) bool {
	if len(current) != len(desired) {
		return false
	}

	currentMap := lo.SliceToMap(current, func(b networkloadbalancer.Backend) (string, networkloadbalancer.Backend) {
		return fmt.Sprintf("%s:%d", lo.FromPtr(b.IpAddress), lo.FromPtr(b.Port)), b
	})

	for _, backend := range desired {
		currentBackend, ok := currentMap[fmt.Sprintf("%s:%d", lo.FromPtr(backend.IpAddress), lo.FromPtr(backend.Port))]
		if !ok ||
			lo.FromPtr(currentBackend.IsDrain) != lo.FromPtr(backend.IsDrain) ||
			lo.FromPtr(currentBackend.Weight) != lo.FromPtr(backend.Weight) {
			return false
		}
	}
	return true
}

func (m *tcpRouteModelImpl) ensureExclusiveListenerOwner(
	ctx context.Context,
	details resolvedTCPRouteDetails,
) error {
	currentRouteKey := tcpRouteKey(details.tcpRoute)
	matches, err := m.matchingRoutesForListener(
		ctx,
		details,
		"",
		"failed to list TCPRoutes for listener ownership check",
	)
	return ensureExclusiveL4ListenerOwner(
		matches,
		currentRouteKey,
		details.matchedListener,
		"TCPRoute",
		func(reason gatewayv1.RouteConditionReason, message string) error {
			return newTCPRouteAcceptedStatusError(reason, message)
		},
		err,
	)
}

func (m *tcpRouteModelImpl) nextEligibleRouteForListener(
	ctx context.Context,
	details resolvedTCPRouteDetails,
) (*resolvedTCPRouteDetails, error) {
	currentRouteKey := tcpRouteKey(details.tcpRoute)
	matches, err := m.matchingRoutesForListener(
		ctx,
		details,
		currentRouteKey,
		"failed to list TCPRoutes for listener failover",
	)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		var missing *resolvedTCPRouteDetails
		return missing, nil
	}
	return &resolvedTCPRouteDetails{
		gatewayDetails:  details.gatewayDetails,
		tcpRoute:        matches[0].route,
		matchedRef:      makeTargetOnlyParentRef(matches[0].matchedRef),
		matchedListener: details.matchedListener,
	}, nil
}

func (m *tcpRouteModelImpl) matchingRoutesForListener(
	ctx context.Context,
	details resolvedTCPRouteDetails,
	excludeRouteKey string,
	listError string,
) ([]l4RouteListenerMatch[gatewayv1alpha2.TCPRoute], error) {
	var routeList gatewayv1alpha2.TCPRouteList
	return listMatchingL4RoutesForListener(ctx, listMatchingL4RoutesForListenerParams[gatewayv1alpha2.TCPRoute]{
		k8sClient:       m.client,
		routeList:       &routeList,
		listError:       listError,
		items:           func() []gatewayv1alpha2.TCPRoute { return routeList.Items },
		gatewayName:     client.ObjectKeyFromObject(&details.gatewayDetails.gateway),
		listener:        details.matchedListener,
		excludeRouteKey: excludeRouteKey,
		routeKey:        tcpRouteKey,
		routeNamespace:  func(route gatewayv1alpha2.TCPRoute) string { return route.Namespace },
		parentRefs:      func(route gatewayv1alpha2.TCPRoute) []gatewayv1.ParentReference { return route.Spec.ParentRefs },
		routeDeleted:    func(route gatewayv1alpha2.TCPRoute) bool { return route.DeletionTimestamp != nil },
		parentTarget:    tcpParentRefTarget,
		matchesListener: tcpRouteMatchesListener,
	})
}

func ensureExclusiveL4ListenerOwner[T any](
	matches []l4RouteListenerMatch[T],
	currentRouteKey string,
	listener gatewayv1.Listener,
	routeKind string,
	statusErr func(gatewayv1.RouteConditionReason, string) error,
	err error,
) error {
	if err != nil {
		return err
	}
	if len(matches) == 0 || matches[0].key == currentRouteKey {
		return nil
	}
	return statusErr(
		gatewayv1.RouteReasonNotAllowedByListeners,
		fmt.Sprintf("listener %s already has an attached %s %s", listener.Name, routeKind, matches[0].key),
	)
}

type l4RouteListenerMatch[T any] struct {
	route      T
	key        string
	matchedRef gatewayv1.ParentReference
}

func matchingL4RoutesForListener[T any](
	routes []T,
	gatewayName apitypes.NamespacedName,
	listener gatewayv1.Listener,
	excludeRouteKey string,
	routeKey func(T) string,
	routeNamespace func(T) string,
	parentRefs func(T) []gatewayv1.ParentReference,
	routeDeleted func(T) bool,
	parentTarget func(gatewayv1.ParentReference, string) apitypes.NamespacedName,
	matchesListener func(gatewayv1.ParentReference, gatewayv1.Listener) bool,
) []l4RouteListenerMatch[T] {
	matches := make([]l4RouteListenerMatch[T], 0)
	for _, route := range routes {
		key := routeKey(route)
		if routeDeleted(route) || key == excludeRouteKey {
			continue
		}
		for _, parentRef := range parentRefs(route) {
			if !parentRefTargetsGateway(parentRef) {
				continue
			}
			if parentTarget(parentRef, routeNamespace(route)) != gatewayName {
				continue
			}
			if matchesListener(parentRef, listener) {
				matches = append(matches, l4RouteListenerMatch[T]{
					route:      route,
					key:        key,
					matchedRef: parentRef,
				})
				break
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].key < matches[j].key
	})
	return matches
}

type listMatchingL4RoutesForListenerParams[T any] struct {
	k8sClient       k8sClient
	routeList       client.ObjectList
	listError       string
	items           func() []T
	gatewayName     apitypes.NamespacedName
	listener        gatewayv1.Listener
	excludeRouteKey string
	routeKey        func(T) string
	routeNamespace  func(T) string
	parentRefs      func(T) []gatewayv1.ParentReference
	routeDeleted    func(T) bool
	parentTarget    func(gatewayv1.ParentReference, string) apitypes.NamespacedName
	matchesListener func(gatewayv1.ParentReference, gatewayv1.Listener) bool
}

func listMatchingL4RoutesForListener[T any](
	ctx context.Context,
	params listMatchingL4RoutesForListenerParams[T],
) ([]l4RouteListenerMatch[T], error) {
	if err := params.k8sClient.List(ctx, params.routeList); err != nil {
		return nil, fmt.Errorf("%s: %w", params.listError, err)
	}
	return matchingL4RoutesForListener(
		params.items(),
		params.gatewayName,
		params.listener,
		params.excludeRouteKey,
		params.routeKey,
		params.routeNamespace,
		params.parentRefs,
		params.routeDeleted,
		params.parentTarget,
		params.matchesListener,
	), nil
}

func (m *tcpRouteModelImpl) clearBackendSet(ctx context.Context, details resolvedTCPRouteDetails) error {
	backendSetName := networkLoadBalancerBackendSetName(details.matchedListener)
	healthChecker := networkLoadBalancerHealthCheckerDetails(
		details.matchedListener.Protocol,
		new(int(details.matchedListener.Port)),
	)
	return m.clearBackendSetByName(
		ctx,
		details.gatewayDetails,
		tcpRouteKey(details.tcpRoute),
		backendSetName,
		&healthChecker,
	)
}

func (m *tcpRouteModelImpl) clearBackendSetByName(
	ctx context.Context,
	gatewayDetails resolvedGatewayDetails,
	routeKey string,
	backendSetName string,
	healthChecker *networkloadbalancer.HealthCheckerDetails,
) error {
	nlb, err := m.networkLoadBalancerModel.getNetworkLoadBalancer(ctx, &gatewayDetails)
	if err != nil {
		return err
	}
	if nlb == nil || nlb.Id == nil {
		m.logger.InfoContext(ctx, "OCI Network Load Balancer is already gone, skipping TCPRoute backend set cleanup",
			slog.String("tcpRoute", routeKey),
		)
		return nil
	}

	if nlb.BackendSets != nil {
		backendSet, found := nlb.BackendSets[backendSetName]
		if !found {
			m.logger.InfoContext(
				ctx,
				"OCI Network Load Balancer backend set is already gone, skipping TCPRoute cleanup",
				slog.String("tcpRoute", routeKey),
				slog.String("backendSetName", backendSetName),
			)
			return nil
		}
		if healthChecker == nil {
			healthChecker = tcpHealthCheckerDetailsFromBackendSet(backendSet)
		}
	}
	if healthChecker == nil {
		healthChecker = &networkloadbalancer.HealthCheckerDetails{
			Protocol: networkloadbalancer.HealthCheckProtocolsTcp,
		}
	}

	return m.operationLocks.withLock(nlb.Id, func() error {
		return updateNetworkLoadBalancerBackendSet(
			ctx,
			m.ociNetworkLoadBalancerAPI,
			m.workRequestsWatcher,
			nlb,
			backendSetName,
			"clear",
			networkloadbalancer.UpdateBackendSetDetails{
				Policy:           new(string(networkloadbalancer.NetworkLoadBalancingPolicyFiveTuple)),
				HealthChecker:    healthChecker,
				IsPreserveSource: new(false),
				Backends:         []networkloadbalancer.BackendDetails{},
			},
		)
	})
}

func tcpHealthCheckerDetailsFromBackendSet(
	backendSet networkloadbalancer.BackendSet,
) *networkloadbalancer.HealthCheckerDetails {
	if backendSet.HealthChecker == nil {
		return nil
	}
	return &networkloadbalancer.HealthCheckerDetails{
		Protocol:          backendSet.HealthChecker.Protocol,
		Port:              backendSet.HealthChecker.Port,
		Retries:           backendSet.HealthChecker.Retries,
		TimeoutInMillis:   backendSet.HealthChecker.TimeoutInMillis,
		IntervalInMillis:  backendSet.HealthChecker.IntervalInMillis,
		UrlPath:           backendSet.HealthChecker.UrlPath,
		ResponseBodyRegex: backendSet.HealthChecker.ResponseBodyRegex,
		ReturnCode:        backendSet.HealthChecker.ReturnCode,
		RequestData:       backendSet.HealthChecker.RequestData,
		ResponseData:      backendSet.HealthChecker.ResponseData,
		Dns:               backendSet.HealthChecker.Dns,
	}
}

func (m *tcpRouteModelImpl) clearStaleBackendSets(
	ctx context.Context,
	details resolvedTCPRouteDetails,
) error {
	programmed := annotatedBackendSetNames(
		&details.tcpRoute,
		NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation,
	)
	desired := desiredTCPRouteBackendSetNames(details)
	for backendSetName := range programmed {
		if _, found := desired[backendSetName]; found {
			continue
		}
		if err := m.clearBackendSetByName(
			ctx,
			details.gatewayDetails,
			tcpRouteKey(details.tcpRoute),
			backendSetName,
			nil,
		); err != nil {
			return err
		}
	}
	return nil
}

func (m *tcpRouteModelImpl) programRoute(
	ctx context.Context,
	details resolvedTCPRouteDetails,
) error {
	return programL4Route(ctx, m.programRouteParams(ctx, details))
}

func (m *tcpRouteModelImpl) programRouteParams(
	ctx context.Context,
	details resolvedTCPRouteDetails,
) programL4RouteParams {
	return newProgramL4RouteParams(newProgramL4RouteParamsInput{
		k8sClient:        m.client,
		routeKind:        "TCPRoute",
		route:            &details.tcpRoute,
		gatewayNamespace: details.gatewayDetails.gateway.Namespace,
		listener:         details.matchedListener,
		finalizer:        NetworkLoadBalancerTCPRouteProgrammedFinalizer,
		clearBackendSet:  func() error { return m.clearBackendSet(ctx, details) },
		ensureOwner:      func() error { return m.ensureExclusiveListenerOwner(ctx, details) },
		clearStale:       func() error { return m.clearStaleBackendSets(ctx, details) },
		resolveBackends: func() ([]networkloadbalancer.BackendDetails, error) {
			return m.endpointBackendsForRoute(ctx, details.tcpRoute)
		},
		isResolvedRefsErr: func(err error) bool {
			var statusErr tcpRouteStatusError
			return errors.As(err, &statusErr) && statusErr.conditionType == gatewayv1.RouteConditionResolvedRefs
		},
		acceptedStatusError: func(reason gatewayv1.RouteConditionReason, message string) error {
			return newTCPRouteAcceptedStatusError(reason, message)
		},
		updateBackendSet: func(name string, backends []networkloadbalancer.BackendDetails) error {
			return m.updateBackendSet(ctx, details, name, backends)
		},
	})
}

type newProgramL4RouteParamsInput struct {
	k8sClient           k8sClient
	routeKind           gatewayv1.Kind
	route               client.Object
	gatewayNamespace    string
	listener            gatewayv1.Listener
	finalizer           string
	clearBackendSet     func() error
	ensureOwner         func() error
	clearStale          func() error
	resolveBackends     func() ([]networkloadbalancer.BackendDetails, error)
	isResolvedRefsErr   func(error) bool
	acceptedStatusError func(gatewayv1.RouteConditionReason, string) error
	updateBackendSet    func(string, []networkloadbalancer.BackendDetails) error
}

func newProgramL4RouteParams(input newProgramL4RouteParamsInput) programL4RouteParams {
	return programL4RouteParams{
		k8sClient:                    input.k8sClient,
		routeKind:                    input.routeKind,
		route:                        input.route,
		gatewayNamespace:             input.gatewayNamespace,
		listener:                     input.listener,
		finalizer:                    input.finalizer,
		clearBackendSet:              input.clearBackendSet,
		ensureExclusiveListenerOwner: input.ensureOwner,
		clearStaleBackendSets:        input.clearStale,
		endpointBackendsForRoute:     input.resolveBackends,
		backendResolutionStatusError: input.isResolvedRefsErr,
		acceptedStatusError:          input.acceptedStatusError,
		updateBackendSet:             input.updateBackendSet,
	}
}

type programL4RouteParams struct {
	k8sClient                    k8sClient
	routeKind                    gatewayv1.Kind
	route                        client.Object
	gatewayNamespace             string
	listener                     gatewayv1.Listener
	finalizer                    string
	clearBackendSet              func() error
	ensureExclusiveListenerOwner func() error
	clearStaleBackendSets        func() error
	endpointBackendsForRoute     func() ([]networkloadbalancer.BackendDetails, error)
	backendResolutionStatusError func(error) bool
	acceptedStatusError          func(gatewayv1.RouteConditionReason, string) error
	updateBackendSet             func(string, []networkloadbalancer.BackendDetails) error
}

func programL4Route(ctx context.Context, params programL4RouteParams) error {
	allowed, err := l4ListenerAllowsRoute(
		ctx,
		params.k8sClient,
		params.gatewayNamespace,
		params.route.GetNamespace(),
		params.listener,
		params.routeKind,
	)
	if err != nil {
		return err
	}
	if !allowed {
		if controllerutil.ContainsFinalizer(params.route, params.finalizer) {
			if cleanupErr := params.clearBackendSet(); cleanupErr != nil {
				return fmt.Errorf(
					"failed to clear backend set after %s attachment was rejected: %w",
					params.routeKind,
					cleanupErr,
				)
			}
		}
		return params.acceptedStatusError(
			gatewayv1.RouteReasonNotAllowedByListeners,
			fmt.Sprintf("listener %s does not allow %s %s/%s",
				params.listener.Name,
				params.routeKind,
				params.route.GetNamespace(),
				params.route.GetName(),
			),
		)
	}

	if ownerErr := params.ensureExclusiveListenerOwner(); ownerErr != nil {
		return ownerErr
	}

	if controllerutil.ContainsFinalizer(params.route, params.finalizer) {
		if cleanupErr := params.clearStaleBackendSets(); cleanupErr != nil {
			return cleanupErr
		}
	}

	backendSetName := networkLoadBalancerBackendSetName(params.listener)
	backends, err := params.endpointBackendsForRoute()
	if err != nil {
		if params.backendResolutionStatusError(err) {
			if cleanupErr := params.clearBackendSet(); cleanupErr != nil {
				return fmt.Errorf(
					"failed to clear backend set after %s backend resolution error: %w",
					params.routeKind,
					cleanupErr,
				)
			}
		}
		return err
	}

	return params.updateBackendSet(backendSetName, backends)
}

func (m *tcpRouteModelImpl) updateBackendSet(
	ctx context.Context,
	details resolvedTCPRouteDetails,
	backendSetName string,
	backends []networkloadbalancer.BackendDetails,
) error {
	lockID := networkLoadBalancerOperationLockID(details.gatewayDetails)
	return m.operationLocks.withLock(lockID, func() error {
		nlb, err := m.networkLoadBalancerModel.ensureNetworkLoadBalancer(ctx, &details.gatewayDetails)
		if err != nil {
			return err
		}
		if err = networkLoadBalancerBusyErrorFromState(nlb); err != nil {
			return err
		}
		if nlb.BackendSets != nil {
			currentBackendSet, ok := nlb.BackendSets[backendSetName]
			if ok &&
				tcpBackendsEqual(currentBackendSet.Backends, backends) &&
				currentBackendSet.IsPreserveSource != nil &&
				!*currentBackendSet.IsPreserveSource {
				m.logger.DebugContext(ctx, "TCPRoute backend set is already up-to-date",
					slog.String("tcpRoute", details.tcpRoute.Name),
					slog.String("backendSetName", backendSetName),
				)
				return nil
			}
		}

		healthChecker := networkLoadBalancerHealthCheckerDetails(details.matchedListener.Protocol, nil)
		m.logger.InfoContext(ctx, "Updating TCPRoute backend set",
			slog.String("tcpRoute", details.tcpRoute.Name),
			slog.String("backendSetName", backendSetName),
			slog.Int("desiredBackends", len(backends)),
		)

		return updateNetworkLoadBalancerBackendSet(
			ctx,
			m.ociNetworkLoadBalancerAPI,
			m.workRequestsWatcher,
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

func (m *tcpRouteModelImpl) deprovisionRoute(
	ctx context.Context,
	details resolvedTCPRouteDetails,
) error {
	return deprovisionL4Route(ctx, deprovisionL4RouteParams[resolvedTCPRouteDetails]{
		k8sClient:          m.client,
		routeKind:          "TCPRoute",
		routeToUpdate:      details.tcpRoute.DeepCopy(),
		finalizer:          NetworkLoadBalancerTCPRouteProgrammedFinalizer,
		backendSetAnnotKey: NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation,
		nextRoute:          func() (*resolvedTCPRouteDetails, error) { return m.nextEligibleRouteForListener(ctx, details) },
		programRoute:       func(route resolvedTCPRouteDetails) error { return m.programRoute(ctx, route) },
		setProgrammed:      func(route resolvedTCPRouteDetails) error { return m.setProgrammed(ctx, route) },
		routeObject:        func(route resolvedTCPRouteDetails) client.Object { return &route.tcpRoute },
		clearBackendSet:    func() error { return m.clearBackendSet(ctx, details) },
	})
}

type deprovisionL4RouteParams[D any] struct {
	k8sClient          k8sClient
	routeKind          string
	routeToUpdate      client.Object
	finalizer          string
	backendSetAnnotKey string
	nextRoute          func() (*D, error)
	programRoute       func(D) error
	setProgrammed      func(D) error
	routeObject        func(D) client.Object
	clearBackendSet    func() error
}

func deprovisionL4Route[D any](ctx context.Context, params deprovisionL4RouteParams[D]) error {
	nextRoute, err := params.nextRoute()
	if err != nil {
		return err
	}
	if nextRoute != nil {
		route := params.routeObject(*nextRoute)
		if err = params.programRoute(*nextRoute); err != nil {
			return fmt.Errorf("failed to program next %s %s/%s: %w",
				params.routeKind,
				route.GetNamespace(),
				route.GetName(),
				err,
			)
		}
		if err = params.setProgrammed(*nextRoute); err != nil {
			return fmt.Errorf("failed to set next %s %s/%s programmed status: %w",
				params.routeKind,
				route.GetNamespace(),
				route.GetName(),
				err,
			)
		}
	} else if err = params.clearBackendSet(); err != nil {
		return err
	}

	controllerutil.RemoveFinalizer(params.routeToUpdate, params.finalizer)
	setAnnotatedBackendSetNames(params.routeToUpdate, params.backendSetAnnotKey, nil)
	if err = params.k8sClient.Update(ctx, params.routeToUpdate); err != nil {
		return fmt.Errorf("failed to remove finalizer from %s %s/%s: %w",
			params.routeKind,
			params.routeToUpdate.GetNamespace(),
			params.routeToUpdate.GetName(),
			err,
		)
	}
	return nil
}

func (m *tcpRouteModelImpl) deprovisionDetachedRoute(
	ctx context.Context,
	route gatewayv1alpha2.TCPRoute,
) error {
	programmedBackendSets := annotatedBackendSetNames(
		&route,
		NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation,
	)
	if len(programmedBackendSets) == 0 {
		return m.removeDetachedRouteFinalizer(ctx, route)
	}

	for _, parentStatus := range route.Status.Parents {
		if parentStatus.ControllerName != gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName) {
			continue
		}
		cleaned, err := m.cleanupDetachedRouteParent(ctx, route, parentStatus, programmedBackendSets)
		if err != nil || cleaned {
			return err
		}
	}

	return nil
}

func (m *tcpRouteModelImpl) cleanupDetachedRouteParent(
	ctx context.Context,
	route gatewayv1alpha2.TCPRoute,
	parentStatus gatewayv1.RouteParentStatus,
	programmedBackendSets map[string]struct{},
) (bool, error) {
	gatewayDetails, resolved, err := m.resolveDetachedRouteGateway(ctx, route, parentStatus)
	if err != nil || !resolved {
		return resolved, err
	}

	for backendSetName := range programmedBackendSets {
		if err = m.clearBackendSetByName(ctx, gatewayDetails, tcpRouteKey(route), backendSetName, nil); err != nil {
			return false, err
		}
	}

	routeToUpdate := route.DeepCopy()
	controllerutil.RemoveFinalizer(routeToUpdate, NetworkLoadBalancerTCPRouteProgrammedFinalizer)
	setAnnotatedBackendSetNames(routeToUpdate, NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation, nil)
	if err = m.client.Update(ctx, routeToUpdate); err != nil {
		return false, fmt.Errorf("failed to update detached TCPRoute %s/%s after cleanup: %w",
			routeToUpdate.Namespace,
			routeToUpdate.Name,
			err,
		)
	}
	return true, nil
}

func (m *tcpRouteModelImpl) resolveDetachedRouteGateway(
	ctx context.Context,
	route gatewayv1alpha2.TCPRoute,
	parentStatus gatewayv1.RouteParentStatus,
) (resolvedGatewayDetails, bool, error) {
	return resolveDetachedL4RouteGateway(
		ctx,
		m.client,
		tcpParentRefTarget(parentStatus.ParentRef, route.Namespace),
		"TCPRoute",
	)
}

func resolveDetachedL4RouteGateway(
	ctx context.Context,
	k8sClient k8sClient,
	gatewayName apitypes.NamespacedName,
	routeKind string,
) (resolvedGatewayDetails, bool, error) {
	var gateway gatewayv1.Gateway
	if err := k8sClient.Get(ctx, gatewayName, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			return resolvedGatewayDetails{}, false, nil
		}
		return resolvedGatewayDetails{}, false, fmt.Errorf(
			"failed to get Gateway %s for detached %s cleanup: %w",
			gatewayName,
			routeKind,
			err,
		)
	}

	var gatewayClass gatewayv1.GatewayClass
	gatewayClassName := apitypes.NamespacedName{Name: string(gateway.Spec.GatewayClassName)}
	if err := k8sClient.Get(ctx, gatewayClassName, &gatewayClass); err != nil {
		if apierrors.IsNotFound(err) {
			return resolvedGatewayDetails{}, false, nil
		}
		return resolvedGatewayDetails{}, false, fmt.Errorf(
			"failed to get GatewayClass %s for detached %s cleanup: %w",
			gateway.Spec.GatewayClassName,
			routeKind,
			err,
		)
	}
	if gatewayClass.Spec.ControllerName != gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName) {
		return resolvedGatewayDetails{}, false, nil
	}
	if gateway.Spec.Infrastructure == nil || gateway.Spec.Infrastructure.ParametersRef == nil {
		return resolvedGatewayDetails{}, false, nil
	}

	var config types.GatewayConfig
	if err := k8sClient.Get(ctx, apitypes.NamespacedName{
		Namespace: gateway.Namespace,
		Name:      gateway.Spec.Infrastructure.ParametersRef.Name,
	}, &config); err != nil {
		if apierrors.IsNotFound(err) {
			return resolvedGatewayDetails{}, false, nil
		}
		return resolvedGatewayDetails{}, false, fmt.Errorf(
			"failed to get GatewayConfig for detached %s cleanup: %w",
			routeKind,
			err,
		)
	}
	return resolvedGatewayDetails{gateway: gateway, gatewayClass: gatewayClass, config: config}, true, nil
}

func (m *tcpRouteModelImpl) removeDetachedRouteFinalizer(
	ctx context.Context,
	route gatewayv1alpha2.TCPRoute,
) error {
	routeToUpdate := route.DeepCopy()
	controllerutil.RemoveFinalizer(routeToUpdate, NetworkLoadBalancerTCPRouteProgrammedFinalizer)
	if err := m.client.Update(ctx, routeToUpdate); err != nil {
		return fmt.Errorf("failed to remove finalizer from detached TCPRoute %s/%s: %w",
			routeToUpdate.Namespace,
			routeToUpdate.Name,
			err,
		)
	}
	return nil
}

func (m *tcpRouteModelImpl) updateParentStatus(
	ctx context.Context,
	details resolvedTCPRouteDetails,
	conditions []metav1.Condition,
) error {
	details.tcpRoute.Status.Parents = mergeL4RouteParentStatus(
		details.tcpRoute.Status.Parents,
		details.matchedRef,
		conditions,
	)

	if err := m.client.Status().Update(ctx, &details.tcpRoute); err != nil {
		return fmt.Errorf("failed to update TCPRoute %s status: %w", details.tcpRoute.Name, err)
	}
	return nil
}

func mergeL4RouteParentStatus(
	parents []gatewayv1.RouteParentStatus,
	parentRef gatewayv1.ParentReference,
	conditions []metav1.Condition,
) []gatewayv1.RouteParentStatus {
	parentStatus := gatewayv1.RouteParentStatus{
		ParentRef:      parentRef,
		ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
		Conditions:     conditions,
	}

	for i, status := range parents {
		if status.ControllerName != parentStatus.ControllerName ||
			!parentRefSameTarget(status.ParentRef, parentStatus.ParentRef) {
			continue
		}
		parents[i].ParentRef = parentStatus.ParentRef
		parents[i].ControllerName = parentStatus.ControllerName
		for _, condition := range parentStatus.Conditions {
			meta.SetStatusCondition(&parents[i].Conditions, condition)
		}
		return parents
	}
	return append(parents, parentStatus)
}

func (m *tcpRouteModelImpl) setProgrammed(ctx context.Context, details resolvedTCPRouteDetails) error {
	routeToUpdate := details.tcpRoute.DeepCopy()
	return setL4RouteProgrammed(ctx, setL4RouteProgrammedParams{
		k8sClient:          m.client,
		routeKind:          "TCPRoute",
		routeToUpdate:      routeToUpdate,
		finalizer:          NetworkLoadBalancerTCPRouteProgrammedFinalizer,
		backendSetAnnotKey: NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation,
		desiredBackendSets: desiredTCPRouteBackendSetNames(details),
		updateParentStatus: func(conditions []metav1.Condition) error {
			return m.updateParentStatus(ctx, resolvedTCPRouteDetails{
				gatewayDetails:  details.gatewayDetails,
				tcpRoute:        *routeToUpdate,
				matchedRef:      details.matchedRef,
				matchedListener: details.matchedListener,
			}, conditions)
		},
	})
}

type setL4RouteProgrammedParams struct {
	k8sClient          k8sClient
	routeKind          string
	routeToUpdate      client.Object
	finalizer          string
	backendSetAnnotKey string
	desiredBackendSets map[string]struct{}
	updateParentStatus func([]metav1.Condition) error
}

func setL4RouteProgrammed(ctx context.Context, params setL4RouteProgrammedParams) error {
	needsUpdate := controllerutil.AddFinalizer(params.routeToUpdate, params.finalizer)
	if len(params.desiredBackendSets) > 0 {
		setAnnotatedBackendSetNames(params.routeToUpdate, params.backendSetAnnotKey, params.desiredBackendSets)
		needsUpdate = true
	}
	if needsUpdate {
		if err := params.k8sClient.Update(ctx, params.routeToUpdate); err != nil {
			return fmt.Errorf("failed to update %s %s/%s finalizer and annotations: %w",
				params.routeKind,
				params.routeToUpdate.GetNamespace(),
				params.routeToUpdate.GetName(),
				err,
			)
		}
	}

	return params.updateParentStatus([]metav1.Condition{
		{
			Type:   string(gatewayv1.RouteConditionAccepted),
			Status: metav1.ConditionTrue,
			Reason: string(gatewayv1.RouteReasonAccepted),
			Message: fmt.Sprintf(
				"%s %s accepted by %s",
				params.routeKind,
				params.routeToUpdate.GetName(),
				NetworkLoadBalancerControllerClassName,
			),
			ObservedGeneration: params.routeToUpdate.GetGeneration(),
			LastTransitionTime: metav1.Now(),
		},
		{
			Type:               string(gatewayv1.RouteConditionResolvedRefs),
			Status:             metav1.ConditionTrue,
			Reason:             string(gatewayv1.RouteReasonResolvedRefs),
			Message:            "Backend references resolved",
			ObservedGeneration: params.routeToUpdate.GetGeneration(),
			LastTransitionTime: metav1.Now(),
		},
	})
}

func (m *tcpRouteModelImpl) setRejected(
	ctx context.Context,
	details resolvedTCPRouteDetails,
	statusErr tcpRouteStatusError,
) error {
	return m.updateParentStatus(ctx, details, []metav1.Condition{
		{
			Type:               string(statusErr.conditionType),
			Status:             metav1.ConditionFalse,
			Reason:             string(statusErr.reason),
			Message:            statusErr.message,
			ObservedGeneration: details.tcpRoute.Generation,
			LastTransitionTime: metav1.Now(),
		},
	})
}

type tcpRouteModelDeps struct {
	dig.In

	RootLogger                *slog.Logger
	K8sClient                 k8sClient
	NetworkLoadBalancerModel  networkLoadBalancerGatewayModel
	OciNetworkLoadBalancerAPI ociNetworkLoadBalancerClient
	WorkRequestsWatcher       workRequestsWatcher `name:"networkLoadBalancerWorkRequestsWatcher"`
	OperationLocks            *networkLoadBalancerOperationLocks
}

func newTCPRouteModel(deps tcpRouteModelDeps) *tcpRouteModelImpl {
	operationLocks := deps.OperationLocks
	if operationLocks == nil {
		operationLocks = newNetworkLoadBalancerOperationLocks()
	}
	watcher := deps.WorkRequestsWatcher
	if watcher == nil {
		watcher = noopWorkRequestsWatcher{}
	}
	return &tcpRouteModelImpl{
		client:                    deps.K8sClient,
		logger:                    deps.RootLogger.WithGroup("tcproute-model"),
		networkLoadBalancerModel:  deps.NetworkLoadBalancerModel,
		ociNetworkLoadBalancerAPI: deps.OciNetworkLoadBalancerAPI,
		workRequestsWatcher:       watcher,
		operationLocks:            operationLocks,
	}
}
