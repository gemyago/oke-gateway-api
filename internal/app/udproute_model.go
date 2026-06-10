package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

type resolvedUDPRouteDetails struct {
	gatewayDetails  resolvedGatewayDetails
	udpRoute        gatewayv1alpha2.UDPRoute
	matchedRef      gatewayv1.ParentReference
	matchedListener gatewayv1.Listener
}

type udpRouteModel interface {
	resolveRequest(ctx context.Context, req reconcile.Request) ([]resolvedUDPRouteDetails, error)
	programRoute(ctx context.Context, details resolvedUDPRouteDetails) error
	deprovisionRoute(ctx context.Context, details resolvedUDPRouteDetails) error
	setProgrammed(ctx context.Context, details resolvedUDPRouteDetails) error
	setRejected(ctx context.Context, details resolvedUDPRouteDetails, statusErr udpRouteStatusError) error
}

type udpRouteStatusError struct {
	conditionType gatewayv1.RouteConditionType
	reason        gatewayv1.RouteConditionReason
	message       string
}

func (e udpRouteStatusError) Error() string {
	return e.message
}

func newUDPRouteAcceptedStatusError(reason gatewayv1.RouteConditionReason, message string) udpRouteStatusError {
	return udpRouteStatusError{
		conditionType: gatewayv1.RouteConditionAccepted,
		reason:        reason,
		message:       message,
	}
}

func newUDPRouteResolvedRefsStatusError(reason gatewayv1.RouteConditionReason, message string) udpRouteStatusError {
	return udpRouteStatusError{
		conditionType: gatewayv1.RouteConditionResolvedRefs,
		reason:        reason,
		message:       message,
	}
}

type udpRouteModelImpl struct {
	client                    k8sClient
	logger                    *slog.Logger
	networkLoadBalancerModel  networkLoadBalancerGatewayModel
	ociNetworkLoadBalancerAPI ociNetworkLoadBalancerClient
	workRequestsWatcher       workRequestsWatcher
	operationLocks            *networkLoadBalancerOperationLocks
}

func udpRouteBackendRefName(backendRef gatewayv1.BackendRef, defaultNamespace string) apitypes.NamespacedName {
	return apitypes.NamespacedName{
		Name: string(backendRef.BackendObjectReference.Name),
		Namespace: lo.IfF(
			backendRef.BackendObjectReference.Namespace != nil,
			func() string { return string(*backendRef.BackendObjectReference.Namespace) },
		).Else(defaultNamespace),
	}
}

func udpParentRefTarget(parentRef gatewayv1.ParentReference, routeNamespace string) apitypes.NamespacedName {
	namespace := routeNamespace
	if parentRef.Namespace != nil {
		namespace = string(*parentRef.Namespace)
	}
	return apitypes.NamespacedName{
		Namespace: namespace,
		Name:      string(parentRef.Name),
	}
}

func udpRouteMatchesListener(parentRef gatewayv1.ParentReference, listener gatewayv1.Listener) bool {
	if listener.Protocol != gatewayv1.UDPProtocolType {
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

func udpRouteKey(route gatewayv1alpha2.UDPRoute) string {
	return fmt.Sprintf("%s/%s", route.Namespace, route.Name)
}

func desiredUDPRouteBackendSetNames(details resolvedUDPRouteDetails) map[string]struct{} {
	desired := make(map[string]struct{})
	gatewayName := apitypes.NamespacedName{
		Namespace: details.gatewayDetails.gateway.Namespace,
		Name:      details.gatewayDetails.gateway.Name,
	}
	for _, parentRef := range details.udpRoute.Spec.ParentRefs {
		if !parentRefTargetsGateway(parentRef) ||
			udpParentRefTarget(parentRef, details.udpRoute.Namespace) != gatewayName {
			continue
		}
		for _, listener := range details.gatewayDetails.gateway.Spec.Listeners {
			if udpRouteMatchesListener(parentRef, listener) {
				desired[networkLoadBalancerBackendSetName(listener)] = struct{}{}
			}
		}
	}
	return desired
}

func (m *udpRouteModelImpl) resolveRequest(
	ctx context.Context,
	req reconcile.Request,
) ([]resolvedUDPRouteDetails, error) {
	route := &gatewayv1alpha2.UDPRoute{}
	return resolveL4RouteRequest(ctx, resolveL4RouteRequestParams[resolvedUDPRouteDetails]{
		k8sClient: m.client,
		logger:    m.logger,
		req:       req,
		routeKind: "UDPRoute",
		route:     route,
		parentRefs: func() []gatewayv1.ParentReference {
			return route.Spec.ParentRefs
		},
		resolveParentRef: func(parentRef gatewayv1.ParentReference) ([]resolvedUDPRouteDetails, bool, error) {
			return m.resolveParentRef(ctx, *route, parentRef)
		},
		rejectNoMatchingListener: func(parentRef gatewayv1.ParentReference) error {
			return m.rejectNoMatchingListener(ctx, *route, parentRef)
		},
		finalizer: NetworkLoadBalancerUDPRouteProgrammedFinalizer,
		handleUnresolvedFinalizedRoute: func() error {
			return m.handleUnresolvedFinalizedRoute(ctx, *route)
		},
	})
}

func (m *udpRouteModelImpl) resolveParentRef(
	ctx context.Context,
	route gatewayv1alpha2.UDPRoute,
	parentRef gatewayv1.ParentReference,
) ([]resolvedUDPRouteDetails, bool, error) {
	gatewayDetails, resolved, err := m.resolveParentGateway(ctx, route.Namespace, parentRef)
	if err != nil || !resolved {
		return nil, resolved, err
	}

	results := make([]resolvedUDPRouteDetails, 0)
	for _, listener := range gatewayDetails.gateway.Spec.Listeners {
		if !udpRouteMatchesListener(parentRef, listener) {
			continue
		}
		results = append(results, resolvedUDPRouteDetails{
			udpRoute:        route,
			gatewayDetails:  gatewayDetails,
			matchedRef:      makeTargetOnlyParentRef(parentRef),
			matchedListener: listener,
		})
	}
	return results, true, nil
}

func (m *udpRouteModelImpl) resolveParentGateway(
	ctx context.Context,
	routeNamespace string,
	parentRef gatewayv1.ParentReference,
) (resolvedGatewayDetails, bool, error) {
	return resolveL4ParentGateway(ctx, m.client, udpParentRefTarget(parentRef, routeNamespace))
}

func (m *udpRouteModelImpl) rejectNoMatchingListener(
	ctx context.Context,
	route gatewayv1alpha2.UDPRoute,
	parentRef gatewayv1.ParentReference,
) error {
	gatewayDetails, resolved, err := m.resolveParentGateway(ctx, route.Namespace, parentRef)
	if err != nil || !resolved {
		return err
	}
	return m.setRejected(ctx, resolvedUDPRouteDetails{
		udpRoute:       route,
		gatewayDetails: gatewayDetails,
		matchedRef:     makeTargetOnlyParentRef(parentRef),
	}, newUDPRouteAcceptedStatusError(
		gatewayv1.RouteReasonNoMatchingParent,
		fmt.Sprintf(
			"Gateway %s/%s has no UDP listener matching this parentRef",
			gatewayDetails.gateway.Namespace,
			gatewayDetails.gateway.Name,
		),
	))
}

func (m *udpRouteModelImpl) handleUnresolvedFinalizedRoute(
	ctx context.Context,
	route gatewayv1alpha2.UDPRoute,
) error {
	if route.DeletionTimestamp != nil {
		return m.removeDeletingRouteFinalizer(ctx, route)
	}
	return m.deprovisionDetachedRoute(ctx, route)
}

func (m *udpRouteModelImpl) removeDeletingRouteFinalizer(
	ctx context.Context,
	route gatewayv1alpha2.UDPRoute,
) error {
	routeToUpdate := route.DeepCopy()
	controllerutil.RemoveFinalizer(routeToUpdate, NetworkLoadBalancerUDPRouteProgrammedFinalizer)
	setAnnotatedBackendSetNames(routeToUpdate, NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation, nil)
	if err := m.client.Update(ctx, routeToUpdate); err != nil {
		return fmt.Errorf("failed to remove finalizer from deleting UDPRoute %s/%s: %w",
			routeToUpdate.Namespace,
			routeToUpdate.Name,
			err,
		)
	}
	return nil
}

func (m *udpRouteModelImpl) endpointBackendsForRoute(
	ctx context.Context,
	route gatewayv1alpha2.UDPRoute,
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

func (m *udpRouteModelImpl) endpointBackendsForBackendRef(
	ctx context.Context,
	route gatewayv1alpha2.UDPRoute,
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

func (m *udpRouteModelImpl) resolveBackendRefServicePort(
	ctx context.Context,
	route gatewayv1alpha2.UDPRoute,
	backendRef gatewayv1.BackendRef,
) (apitypes.NamespacedName, *corev1.ServicePort, error) {
	return resolveL4BackendRefServicePort(
		ctx,
		m.client,
		gatewayv1.Kind("UDPRoute"),
		route.Namespace,
		backendRef,
		udpRouteBackendRefName(backendRef, route.Namespace),
		func(reason gatewayv1.RouteConditionReason, message string) error {
			return newUDPRouteResolvedRefsStatusError(reason, message)
		},
	)
}

func udpBackendsEqual(current []networkloadbalancer.Backend, desired []networkloadbalancer.BackendDetails) bool {
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

func (m *udpRouteModelImpl) ensureExclusiveListenerOwner(
	ctx context.Context,
	details resolvedUDPRouteDetails,
) error {
	currentRouteKey := udpRouteKey(details.udpRoute)
	matches, err := m.matchingRoutesForListener(
		ctx,
		details,
		"",
		"failed to list UDPRoutes for listener ownership check",
	)
	return ensureExclusiveL4ListenerOwner(
		matches,
		currentRouteKey,
		details.matchedListener,
		"UDPRoute",
		func(reason gatewayv1.RouteConditionReason, message string) error {
			return newUDPRouteAcceptedStatusError(reason, message)
		},
		err,
	)
}

func (m *udpRouteModelImpl) nextEligibleRouteForListener(
	ctx context.Context,
	details resolvedUDPRouteDetails,
) (*resolvedUDPRouteDetails, error) {
	currentRouteKey := udpRouteKey(details.udpRoute)
	matches, err := m.matchingRoutesForListener(
		ctx,
		details,
		currentRouteKey,
		"failed to list UDPRoutes for listener failover",
	)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		var missing *resolvedUDPRouteDetails
		return missing, nil
	}
	return &resolvedUDPRouteDetails{
		gatewayDetails:  details.gatewayDetails,
		udpRoute:        matches[0].route,
		matchedRef:      makeTargetOnlyParentRef(matches[0].matchedRef),
		matchedListener: details.matchedListener,
	}, nil
}

func (m *udpRouteModelImpl) matchingRoutesForListener(
	ctx context.Context,
	details resolvedUDPRouteDetails,
	excludeRouteKey string,
	listError string,
) ([]l4RouteListenerMatch[gatewayv1alpha2.UDPRoute], error) {
	var routeList gatewayv1alpha2.UDPRouteList
	params := listMatchingL4RoutesForListenerParams[gatewayv1alpha2.UDPRoute]{
		k8sClient:    m.client,
		routeList:    &routeList,
		listError:    listError,
		items:        func() []gatewayv1alpha2.UDPRoute { return routeList.Items },
		routeKey:     udpRouteKey,
		parentTarget: udpParentRefTarget,
	}
	params.gatewayName = client.ObjectKeyFromObject(&details.gatewayDetails.gateway)
	params.listener = details.matchedListener
	params.excludeRouteKey = excludeRouteKey
	params.routeNamespace = func(route gatewayv1alpha2.UDPRoute) string { return route.Namespace }
	params.parentRefs = func(route gatewayv1alpha2.UDPRoute) []gatewayv1.ParentReference { return route.Spec.ParentRefs }
	params.routeDeleted = func(route gatewayv1alpha2.UDPRoute) bool { return route.DeletionTimestamp != nil }
	params.matchesListener = udpRouteMatchesListener
	return listMatchingL4RoutesForListener(ctx, params)
}

func (m *udpRouteModelImpl) clearBackendSet(ctx context.Context, details resolvedUDPRouteDetails) error {
	backendSetName := networkLoadBalancerBackendSetName(details.matchedListener)
	healthChecker := networkLoadBalancerHealthCheckerDetails(
		details.matchedListener.Protocol,
		new(int(details.matchedListener.Port)),
	)
	return m.clearBackendSetByName(
		ctx,
		details.gatewayDetails,
		udpRouteKey(details.udpRoute),
		backendSetName,
		&healthChecker,
	)
}

func (m *udpRouteModelImpl) clearBackendSetByName(
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
		m.logger.InfoContext(ctx, "OCI Network Load Balancer is already gone, skipping UDPRoute backend set cleanup",
			slog.String("udpRoute", routeKey),
		)
		return nil
	}

	if nlb.BackendSets != nil {
		backendSet, found := nlb.BackendSets[backendSetName]
		if !found {
			m.logger.InfoContext(
				ctx,
				"OCI Network Load Balancer backend set is already gone, skipping UDPRoute cleanup",
				slog.String("udpRoute", routeKey),
				slog.String("backendSetName", backendSetName),
			)
			return nil
		}
		if healthChecker == nil {
			healthChecker = udpHealthCheckerDetailsFromBackendSet(backendSet)
		}
	}
	if healthChecker == nil {
		defaultHealthChecker := networkLoadBalancerHealthCheckerDetails(gatewayv1.UDPProtocolType, nil)
		healthChecker = &defaultHealthChecker
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

func udpHealthCheckerDetailsFromBackendSet(
	backendSet networkloadbalancer.BackendSet,
) *networkloadbalancer.HealthCheckerDetails {
	if backendSet.HealthChecker == nil {
		return nil
	}
	healthChecker := &networkloadbalancer.HealthCheckerDetails{
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
	if healthChecker.Protocol == networkloadbalancer.HealthCheckProtocolsUdp {
		healthChecker.Protocol = networkloadbalancer.HealthCheckProtocolsTcp
		healthChecker.RequestData = nil
		healthChecker.ResponseData = nil
	}
	return healthChecker
}

func udpBackendSetUsesTCPHealthChecker(backendSet networkloadbalancer.BackendSet) bool {
	return backendSet.HealthChecker != nil &&
		backendSet.HealthChecker.Protocol == networkloadbalancer.HealthCheckProtocolsTcp
}

func udpHealthCheckPort(details resolvedUDPRouteDetails) (int, error) {
	value := details.udpRoute.GetAnnotations()[NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation]
	if value == "" {
		return 0, newUDPRouteAcceptedStatusError(
			gatewayv1.RouteReasonUnsupportedValue,
			fmt.Sprintf(
				"annotation %s is required and must be a TCP port between 1 and 65535",
				NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation,
			),
		)
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, newUDPRouteAcceptedStatusError(
			gatewayv1.RouteReasonUnsupportedValue,
			fmt.Sprintf(
				"annotation %s must be a TCP port between 1 and 65535",
				NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation,
			),
		)
	}
	return port, nil
}

func udpBackendSetUsesHealthChecker(
	backendSet networkloadbalancer.BackendSet,
	healthChecker networkloadbalancer.HealthCheckerDetails,
) bool {
	if !udpBackendSetUsesTCPHealthChecker(backendSet) {
		return false
	}
	if healthChecker.Port == nil {
		return backendSet.HealthChecker.Port == nil
	}
	return backendSet.HealthChecker.Port != nil && *backendSet.HealthChecker.Port == *healthChecker.Port
}

func (m *udpRouteModelImpl) clearStaleBackendSets(
	ctx context.Context,
	details resolvedUDPRouteDetails,
) error {
	programmed := annotatedBackendSetNames(
		&details.udpRoute,
		NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation,
	)
	desired := desiredUDPRouteBackendSetNames(details)
	for backendSetName := range programmed {
		if _, found := desired[backendSetName]; found {
			continue
		}
		if err := m.clearBackendSetByName(
			ctx,
			details.gatewayDetails,
			udpRouteKey(details.udpRoute),
			backendSetName,
			nil,
		); err != nil {
			return err
		}
	}
	return nil
}

func (m *udpRouteModelImpl) programRoute(
	ctx context.Context,
	details resolvedUDPRouteDetails,
) error {
	return programL4Route(ctx, m.programRouteParams(ctx, details))
}

func (m *udpRouteModelImpl) programRouteParams(
	ctx context.Context,
	details resolvedUDPRouteDetails,
) programL4RouteParams {
	input := newProgramL4RouteParamsInput{
		k8sClient:   m.client,
		routeKind:   "UDPRoute",
		route:       &details.udpRoute,
		listener:    details.matchedListener,
		finalizer:   NetworkLoadBalancerUDPRouteProgrammedFinalizer,
		ensureOwner: func() error { return m.ensureExclusiveListenerOwner(ctx, details) },
		clearStale:  func() error { return m.clearStaleBackendSets(ctx, details) },
		resolveBackends: func() ([]networkloadbalancer.BackendDetails, error) {
			return m.endpointBackendsForRoute(ctx, details.udpRoute)
		},
	}
	input.gatewayNamespace = details.gatewayDetails.gateway.Namespace
	input.clearBackendSet = func() error { return m.clearBackendSet(ctx, details) }
	input.isResolvedRefsErr = func(err error) bool {
		var statusErr udpRouteStatusError
		return errors.As(err, &statusErr) && statusErr.conditionType == gatewayv1.RouteConditionResolvedRefs
	}
	input.acceptedStatusError = func(reason gatewayv1.RouteConditionReason, message string) error {
		return newUDPRouteAcceptedStatusError(reason, message)
	}
	input.updateBackendSet = func(name string, backends []networkloadbalancer.BackendDetails) error {
		return m.updateBackendSet(ctx, details, name, backends)
	}
	return newProgramL4RouteParams(input)
}

func (m *udpRouteModelImpl) updateBackendSet(
	ctx context.Context,
	details resolvedUDPRouteDetails,
	backendSetName string,
	backends []networkloadbalancer.BackendDetails,
) error {
	lockID := networkLoadBalancerOperationLockID(details.gatewayDetails)
	return m.operationLocks.withLock(lockID, func() error {
		healthCheckPort, err := udpHealthCheckPort(details)
		if err != nil {
			return err
		}
		healthChecker := networkLoadBalancerHealthCheckerDetails(
			details.matchedListener.Protocol,
			new(healthCheckPort),
		)
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
				udpBackendsEqual(currentBackendSet.Backends, backends) &&
				currentBackendSet.IsPreserveSource != nil &&
				!*currentBackendSet.IsPreserveSource &&
				udpBackendSetUsesHealthChecker(currentBackendSet, healthChecker) {
				m.logger.DebugContext(ctx, "UDPRoute backend set is already up-to-date",
					slog.String("udpRoute", details.udpRoute.Name),
					slog.String("backendSetName", backendSetName),
				)
				return nil
			}
		}

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

func (m *udpRouteModelImpl) deprovisionRoute(
	ctx context.Context,
	details resolvedUDPRouteDetails,
) error {
	return deprovisionL4Route(ctx, deprovisionL4RouteParams[resolvedUDPRouteDetails]{
		k8sClient:          m.client,
		routeKind:          "UDPRoute",
		routeToUpdate:      details.udpRoute.DeepCopy(),
		finalizer:          NetworkLoadBalancerUDPRouteProgrammedFinalizer,
		backendSetAnnotKey: NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation,
		nextRoute:          func() (*resolvedUDPRouteDetails, error) { return m.nextEligibleRouteForListener(ctx, details) },
		programRoute:       func(route resolvedUDPRouteDetails) error { return m.programRoute(ctx, route) },
		setProgrammed:      func(route resolvedUDPRouteDetails) error { return m.setProgrammed(ctx, route) },
		routeObject:        func(route resolvedUDPRouteDetails) client.Object { return &route.udpRoute },
		clearBackendSet:    func() error { return m.clearBackendSet(ctx, details) },
	})
}

func (m *udpRouteModelImpl) deprovisionDetachedRoute(
	ctx context.Context,
	route gatewayv1alpha2.UDPRoute,
) error {
	programmedBackendSets := annotatedBackendSetNames(
		&route,
		NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation,
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

func (m *udpRouteModelImpl) cleanupDetachedRouteParent(
	ctx context.Context,
	route gatewayv1alpha2.UDPRoute,
	parentStatus gatewayv1.RouteParentStatus,
	programmedBackendSets map[string]struct{},
) (bool, error) {
	gatewayDetails, resolved, err := m.resolveDetachedRouteGateway(ctx, route, parentStatus)
	if err != nil || !resolved {
		return resolved, err
	}

	for backendSetName := range programmedBackendSets {
		if err = m.clearBackendSetByName(ctx, gatewayDetails, udpRouteKey(route), backendSetName, nil); err != nil {
			return false, err
		}
	}

	routeToUpdate := route.DeepCopy()
	controllerutil.RemoveFinalizer(routeToUpdate, NetworkLoadBalancerUDPRouteProgrammedFinalizer)
	setAnnotatedBackendSetNames(routeToUpdate, NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation, nil)
	if err = m.client.Update(ctx, routeToUpdate); err != nil {
		return false, fmt.Errorf("failed to update detached UDPRoute %s/%s after cleanup: %w",
			routeToUpdate.Namespace,
			routeToUpdate.Name,
			err,
		)
	}
	return true, nil
}

func (m *udpRouteModelImpl) resolveDetachedRouteGateway(
	ctx context.Context,
	route gatewayv1alpha2.UDPRoute,
	parentStatus gatewayv1.RouteParentStatus,
) (resolvedGatewayDetails, bool, error) {
	return resolveDetachedL4RouteGateway(
		ctx,
		m.client,
		udpParentRefTarget(parentStatus.ParentRef, route.Namespace),
		"UDPRoute",
	)
}

func (m *udpRouteModelImpl) removeDetachedRouteFinalizer(
	ctx context.Context,
	route gatewayv1alpha2.UDPRoute,
) error {
	routeToUpdate := route.DeepCopy()
	controllerutil.RemoveFinalizer(routeToUpdate, NetworkLoadBalancerUDPRouteProgrammedFinalizer)
	if err := m.client.Update(ctx, routeToUpdate); err != nil {
		return fmt.Errorf("failed to remove finalizer from detached UDPRoute %s/%s: %w",
			routeToUpdate.Namespace,
			routeToUpdate.Name,
			err,
		)
	}
	return nil
}

func (m *udpRouteModelImpl) updateParentStatus(
	ctx context.Context,
	details resolvedUDPRouteDetails,
	conditions []metav1.Condition,
) error {
	details.udpRoute.Status.Parents = mergeL4RouteParentStatus(
		details.udpRoute.Status.Parents,
		details.matchedRef,
		conditions,
	)

	if err := m.client.Status().Update(ctx, &details.udpRoute); err != nil {
		return fmt.Errorf("failed to update UDPRoute %s status: %w", details.udpRoute.Name, err)
	}
	return nil
}

func (m *udpRouteModelImpl) setProgrammed(ctx context.Context, details resolvedUDPRouteDetails) error {
	routeToUpdate := details.udpRoute.DeepCopy()
	return setL4RouteProgrammed(ctx, setL4RouteProgrammedParams{
		k8sClient:          m.client,
		routeKind:          "UDPRoute",
		routeToUpdate:      routeToUpdate,
		finalizer:          NetworkLoadBalancerUDPRouteProgrammedFinalizer,
		backendSetAnnotKey: NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation,
		desiredBackendSets: desiredUDPRouteBackendSetNames(details),
		updateParentStatus: func(conditions []metav1.Condition) error {
			return m.updateParentStatus(ctx, resolvedUDPRouteDetails{
				gatewayDetails:  details.gatewayDetails,
				udpRoute:        *routeToUpdate,
				matchedRef:      details.matchedRef,
				matchedListener: details.matchedListener,
			}, conditions)
		},
	})
}

func (m *udpRouteModelImpl) setRejected(
	ctx context.Context,
	details resolvedUDPRouteDetails,
	statusErr udpRouteStatusError,
) error {
	return m.updateParentStatus(ctx, details, []metav1.Condition{
		{
			Type:               string(statusErr.conditionType),
			Status:             metav1.ConditionFalse,
			Reason:             string(statusErr.reason),
			Message:            statusErr.message,
			ObservedGeneration: details.udpRoute.Generation,
			LastTransitionTime: metav1.Now(),
		},
	})
}

type udpRouteModelDeps struct {
	dig.In

	RootLogger                *slog.Logger
	K8sClient                 k8sClient
	NetworkLoadBalancerModel  networkLoadBalancerGatewayModel
	OciNetworkLoadBalancerAPI ociNetworkLoadBalancerClient
	WorkRequestsWatcher       workRequestsWatcher `name:"networkLoadBalancerWorkRequestsWatcher"`
	OperationLocks            *networkLoadBalancerOperationLocks
}

func newUDPRouteModel(deps udpRouteModelDeps) *udpRouteModelImpl {
	operationLocks := deps.OperationLocks
	if operationLocks == nil {
		operationLocks = newNetworkLoadBalancerOperationLocks()
	}
	return &udpRouteModelImpl{
		client:                    deps.K8sClient,
		logger:                    deps.RootLogger.WithGroup("udproute-model"),
		networkLoadBalancerModel:  deps.NetworkLoadBalancerModel,
		ociNetworkLoadBalancerAPI: deps.OciNetworkLoadBalancerAPI,
		workRequestsWatcher:       deps.WorkRequestsWatcher,
		operationLocks:            operationLocks,
	}
}
