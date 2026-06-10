package app

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/types"
)

type resolvedGRPCRouteDetails struct {
	gatewayDetails   resolvedGatewayDetails
	grpcRoute        gatewayv1.GRPCRoute
	matchedRef       gatewayv1.ParentReference
	matchedListeners []gatewayv1.Listener
}

type resolveGRPCBackendRefsParams struct {
	grpcRoute gatewayv1.GRPCRoute
}

type programGRPCRouteParams struct {
	gateway          gatewayv1.Gateway
	config           types.GatewayConfig
	grpcRoute        gatewayv1.GRPCRoute
	knownBackends    map[string]corev1.Service
	matchedListeners []gatewayv1.Listener
}

type programGRPCRouteResult struct {
	programmedPolicyRules []string
}

type deprovisionGRPCRouteParams struct {
	config           types.GatewayConfig
	grpcRoute        gatewayv1.GRPCRoute
	matchedListeners []gatewayv1.Listener
}

type setGRPCRouteProgrammedParams struct {
	grpcRoute    gatewayv1.GRPCRoute
	gatewayClass gatewayv1.GatewayClass
	gateway      gatewayv1.Gateway
	matchedRef   gatewayv1.ParentReference

	programmedPolicyRules []string
}

type grpcRouteModel interface {
	resolveRequest(
		ctx context.Context,
		req reconcile.Request,
	) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error)

	acceptRoute(
		ctx context.Context,
		routeDetails resolvedGRPCRouteDetails,
	) (*gatewayv1.GRPCRoute, error)

	resolveBackendRefs(
		ctx context.Context,
		params resolveGRPCBackendRefsParams,
	) (map[string]corev1.Service, error)

	isProgrammingRequired(details resolvedGRPCRouteDetails) bool

	programRoute(
		ctx context.Context,
		params programGRPCRouteParams,
	) (programGRPCRouteResult, error)

	deprovisionRoute(
		ctx context.Context,
		params deprovisionGRPCRouteParams,
	) error

	setProgrammed(
		ctx context.Context,
		params setGRPCRouteProgrammedParams,
	) error
}

type grpcRouteModelImpl struct {
	client               k8sClient
	logger               *slog.Logger
	gatewayModel         gatewayModel
	resourcesModel       resourcesModel
	ociLoadBalancerModel ociLoadBalancerModel
}

func (m *grpcRouteModelImpl) resolveRouteParentRefData(
	ctx context.Context,
	grpcRoute gatewayv1.GRPCRoute,
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
	m.logger.DebugContext(ctx, "Resolving parent for GRPCRoute",
		slog.String("parentName", parentName.String()),
		slog.Any("parentRef", parentRef),
		slog.String("route", apitypes.NamespacedName{
			Namespace: grpcRoute.Namespace,
			Name:      grpcRoute.Name,
		}.String()),
	)

	gatewayResolved, err := m.gatewayModel.resolveReconcileRequest(ctx, reconcile.Request{
		NamespacedName: parentName,
	}, &resolvedGatewayData)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve gateway %s for route %s/%s: %w",
			parentName.String(), grpcRoute.Namespace, grpcRoute.Name, err)
	}
	if !gatewayResolved {
		return nil, nil, nil
	}

	if parentRef.SectionName != nil {
		sectionName := *parentRef.SectionName
		matchingListeners := lo.Filter(
			resolvedGatewayData.gateway.Spec.Listeners,
			func(listener gatewayv1.Listener, _ int) bool {
				return listener.Name == sectionName && grpcRouteListenerProtocolSupported(listener.Protocol)
			},
		)
		if len(matchingListeners) == 0 {
			return nil, nil, nil
		}
		return &resolvedGatewayData, matchingListeners, nil
	}

	matchingListeners := lo.Filter(
		resolvedGatewayData.gateway.Spec.Listeners,
		func(listener gatewayv1.Listener, _ int) bool {
			return grpcRouteListenerProtocolSupported(listener.Protocol)
		},
	)
	return &resolvedGatewayData, matchingListeners, nil
}

func grpcRouteListenerProtocolSupported(protocol gatewayv1.ProtocolType) bool {
	return protocol == gatewayv1.HTTPProtocolType || protocol == gatewayv1.HTTPSProtocolType
}

func (m *grpcRouteModelImpl) aggregateRouteParentRefData(
	ctx context.Context,
	results map[apitypes.NamespacedName]resolvedGRPCRouteDetails,
	grpcRoute gatewayv1.GRPCRoute,
	gatewayDetails resolvedGatewayDetails,
	matchedRef gatewayv1.ParentReference,
	matchedListeners []gatewayv1.Listener,
) {
	parentName := apitypes.NamespacedName{
		Namespace: gatewayDetails.gateway.Namespace,
		Name:      gatewayDetails.gateway.Name,
	}

	if existingResult, found := results[parentName]; found {
		existingResult.matchedListeners = lo.UniqBy(
			append(existingResult.matchedListeners, matchedListeners...),
			func(listener gatewayv1.Listener) gatewayv1.SectionName {
				return listener.Name
			},
		)
		results[parentName] = existingResult
		m.logger.DebugContext(ctx, "Appended/merged listeners for existing GRPCRoute gateway result",
			slog.String("parentName", parentName.String()),
			slog.Int("totalListeners", len(existingResult.matchedListeners)),
		)
		return
	}

	results[parentName] = resolvedGRPCRouteDetails{
		grpcRoute:        grpcRoute,
		gatewayDetails:   gatewayDetails,
		matchedRef:       matchedRef,
		matchedListeners: matchedListeners,
	}
}

func (m *grpcRouteModelImpl) resolveRequest(
	ctx context.Context,
	req reconcile.Request,
) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
	var grpcRoute gatewayv1.GRPCRoute
	if err := m.client.Get(ctx, req.NamespacedName, &grpcRoute); err != nil {
		if apierrors.IsNotFound(err) {
			return map[apitypes.NamespacedName]resolvedGRPCRouteDetails{}, nil
		}
		return nil, fmt.Errorf("failed to get GRPCRoute %s: %w", req.NamespacedName.String(), err)
	}

	results := make(map[apitypes.NamespacedName]resolvedGRPCRouteDetails)
	for _, parentRef := range grpcRoute.Spec.ParentRefs {
		resolvedGatewayData, matchedListeners, err := m.resolveRouteParentRefData(
			ctx,
			grpcRoute,
			parentRef,
			req.NamespacedName.Namespace,
		)
		if err != nil {
			return nil, err
		}
		if resolvedGatewayData != nil {
			m.aggregateRouteParentRefData(
				ctx,
				results,
				grpcRoute,
				*resolvedGatewayData,
				makeTargetOnlyParentRef(parentRef),
				matchedListeners,
			)
		}
	}

	return results, nil
}

func (m *grpcRouteModelImpl) acceptRoute(
	ctx context.Context,
	routeDetails resolvedGRPCRouteDetails,
) (*gatewayv1.GRPCRoute, error) {
	winner, conflicted, err := checkL7RouteConflict(ctx, checkL7RouteConflictParams{
		gateway:          routeDetails.gatewayDetails.gateway,
		matchedListeners: routeDetails.matchedListeners,
		current: l7RouteCandidate{
			identity: l7RouteIdentity{
				kind:              l7GRPCRouteKind,
				namespace:         routeDetails.grpcRoute.Namespace,
				name:              routeDetails.grpcRoute.Name,
				creationTimestamp: routeDetails.grpcRoute.CreationTimestamp,
			},
			parentRefs: routeDetails.grpcRoute.Spec.ParentRefs,
			hostnames:  routeDetails.grpcRoute.Spec.Hostnames,
		},
		oppositeRouteListName: "HTTPRoutes",
		listOppositeRoutes:    m.listHTTPRouteConflictCandidates,
	})
	if err != nil {
		return nil, err
	}
	if conflicted {
		return nil, m.rejectRoute(ctx, routeDetails, l7RouteConflictMessage(winner))
	}

	parentStatus, parentStatusIndex, found := lo.FindIndexOf(
		routeDetails.grpcRoute.Status.Parents,
		func(status gatewayv1.RouteParentStatus) bool {
			return status.ControllerName == routeDetails.gatewayDetails.gatewayClass.Spec.ControllerName &&
				parentRefSameTarget(status.ParentRef, routeDetails.matchedRef)
		})
	if found {
		existingCondition := meta.FindStatusCondition(
			parentStatus.Conditions,
			string(gatewayv1.RouteConditionAccepted),
		)
		if existingCondition != nil &&
			existingCondition.ObservedGeneration == routeDetails.grpcRoute.Generation &&
			existingCondition.Status == metav1.ConditionTrue {
			return &routeDetails.grpcRoute, nil
		}
	} else {
		parentStatus = gatewayv1.RouteParentStatus{
			ParentRef:      makeTargetOnlyParentRef(routeDetails.matchedRef),
			ControllerName: routeDetails.gatewayDetails.gatewayClass.Spec.ControllerName,
		}
	}

	grpcRoute := routeDetails.grpcRoute.DeepCopy()
	meta.SetStatusCondition(&parentStatus.Conditions, metav1.Condition{
		Type:               string(gatewayv1.RouteConditionAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             string(gatewayv1.RouteReasonAccepted),
		ObservedGeneration: grpcRoute.Generation,
		LastTransitionTime: metav1.Now(),
		Message:            fmt.Sprintf("Route accepted by %s", routeDetails.gatewayDetails.gateway.Name),
	})

	if found {
		grpcRoute.Status.Parents[parentStatusIndex] = parentStatus
	} else {
		grpcRoute.Status.Parents = append(grpcRoute.Status.Parents, parentStatus)
	}

	if updateErr := m.client.Status().Update(ctx, grpcRoute); updateErr != nil {
		return nil, fmt.Errorf("failed to update status for GRPCRoute %s: %w", grpcRoute.Name, updateErr)
	}

	return grpcRoute, nil
}

func (m *grpcRouteModelImpl) listHTTPRouteConflictCandidates(ctx context.Context) ([]l7RouteCandidate, error) {
	var httpRoutes gatewayv1.HTTPRouteList
	if err := m.client.List(ctx, &httpRoutes); err != nil {
		return nil, err
	}
	return lo.FilterMap(httpRoutes.Items, func(route gatewayv1.HTTPRoute, _ int) (l7RouteCandidate, bool) {
		if route.DeletionTimestamp != nil {
			return l7RouteCandidate{}, false
		}
		return l7RouteCandidate{
			identity: l7RouteIdentity{
				kind:              l7HTTPRouteKind,
				namespace:         route.Namespace,
				name:              route.Name,
				creationTimestamp: route.CreationTimestamp,
			},
			parentRefs: route.Spec.ParentRefs,
			hostnames:  route.Spec.Hostnames,
		}, true
	}), nil
}

func (m *grpcRouteModelImpl) rejectRoute(
	ctx context.Context,
	routeDetails resolvedGRPCRouteDetails,
	message string,
) error {
	grpcRoute := routeDetails.grpcRoute.DeepCopy()
	if programmedPolicyRulesAnnotation, ok := grpcRoute.Annotations[GRPCRouteProgrammedPolicyRulesAnnotation]; ok {
		if err := removeL7RoutePolicyRules(
			ctx,
			m.ociLoadBalancerModel,
			routeDetails.gatewayDetails.config.Spec.LoadBalancerID,
			routeDetails.matchedListeners,
			programmedPolicyRulesAnnotation,
		); err != nil {
			return fmt.Errorf("failed to remove rejected GRPCRoute policy rules: %w", err)
		}
	}

	return rejectL7Route(ctx, m.client, rejectL7RouteParams{
		resource:       grpcRoute,
		parentStatuses: &grpcRoute.Status.Parents,
		gatewayClass:   routeDetails.gatewayDetails.gatewayClass,
		matchedRef:     routeDetails.matchedRef,
		message:        message,
		routeKind:      "GRPCRoute",
	})
}

func (m *grpcRouteModelImpl) resolveBackendRefs(
	ctx context.Context,
	params resolveGRPCBackendRefsParams,
) (map[string]corev1.Service, error) {
	resolvedBackendRefs := make(map[string]corev1.Service)
	for _, rule := range params.grpcRoute.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			fullName := backendObjectRefName(backendRef.BackendObjectReference, params.grpcRoute.Namespace)

			var service corev1.Service
			if err := m.client.Get(ctx, fullName, &service); err != nil {
				return nil, fmt.Errorf("failed to get service %s: %w", fullName.String(), err)
			}

			resolvedBackendRefs[fullName.String()] = service
		}
	}

	return resolvedBackendRefs, nil
}

func (m *grpcRouteModelImpl) programRoute(
	ctx context.Context,
	params programGRPCRouteParams,
) (programGRPCRouteResult, error) {
	var previousRules []programmedHTTPRoutePolicyRule
	if prevPolicyRulesStr, ok := params.grpcRoute.Annotations[GRPCRouteProgrammedPolicyRulesAnnotation]; ok {
		previousRules = parseProgrammedHTTPRoutePolicyRules(prevPolicyRulesStr)
	}

	routePolicyParams := programL7RoutePolicyParams{
		loadBalancerID:      params.config.Spec.LoadBalancerID,
		routeName:           params.grpcRoute.Name,
		routeNamespace:      params.grpcRoute.Namespace,
		backendRefs:         grpcRouteBackendRefs(params.grpcRoute),
		knownBackends:       params.knownBackends,
		matchedListeners:    params.matchedListeners,
		previousPolicyRules: previousRules,
		ruleCount:           len(params.grpcRoute.Spec.Rules),
		makeRoutingRule: func(ruleIndex int) (loadbalancer.RoutingRule, error) {
			return m.ociLoadBalancerModel.makeGRPCRoutingRule(ctx, makeGRPCRoutingRuleParams{
				grpcRoute:          params.grpcRoute,
				grpcRouteRuleIndex: ruleIndex,
			})
		},
	}

	programmedPolicyRules, err := programL7RoutePolicy(ctx, m.ociLoadBalancerModel, routePolicyParams)
	if err != nil {
		return programGRPCRouteResult{}, err
	}

	return programGRPCRouteResult{
		programmedPolicyRules: programmedPolicyRules,
	}, nil
}

func grpcRouteBackendRefs(route gatewayv1.GRPCRoute) []gatewayv1.BackendRef {
	backendRefs := make([]gatewayv1.BackendRef, 0)
	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			backendRefs = append(backendRefs, backendRef.BackendRef)
		}
	}
	return backendRefs
}

func (m *grpcRouteModelImpl) deprovisionRoute(
	ctx context.Context,
	params deprovisionGRPCRouteParams,
) error {
	var previousRules []programmedHTTPRoutePolicyRule
	if prevPolicyRulesStr, ok := params.grpcRoute.Annotations[GRPCRouteProgrammedPolicyRulesAnnotation]; ok {
		previousRules = parseProgrammedHTTPRoutePolicyRules(prevPolicyRulesStr)
	}

	prevRulesByListener := previousPolicyRulesByListener(previousRules, params.matchedListeners)
	listenerNames := lo.Keys(prevRulesByListener)
	sort.Strings(listenerNames)
	for _, listenerName := range listenerNames {
		err := m.ociLoadBalancerModel.commitRoutingPolicy(ctx, commitRoutingPolicyParams{
			loadBalancerID:  params.config.Spec.LoadBalancerID,
			listenerName:    listenerName,
			policyRules:     []loadbalancer.RoutingRule{},
			prevPolicyRules: prevRulesByListener[listenerName],
		})
		if err != nil {
			return fmt.Errorf("failed to deprovision routing policy for listener %s: %w", listenerName, err)
		}
	}

	routeToUpdate := params.grpcRoute.DeepCopy()
	controllerutil.RemoveFinalizer(routeToUpdate, GRPCRouteProgrammedFinalizer)

	if err := m.client.Update(ctx, routeToUpdate); err != nil {
		return fmt.Errorf("failed to update GRPCRoute %s/%s after deprovisioning: %w",
			routeToUpdate.Namespace, routeToUpdate.Name, err)
	}

	return nil
}

func (m *grpcRouteModelImpl) isProgrammingRequired(details resolvedGRPCRouteDetails) bool {
	parentStatus, found := lo.Find(details.grpcRoute.Status.Parents, func(status gatewayv1.RouteParentStatus) bool {
		return status.ControllerName == details.gatewayDetails.gatewayClass.Spec.ControllerName &&
			parentRefSameTarget(status.ParentRef, details.matchedRef)
	})
	if !found {
		return true
	}

	return !m.resourcesModel.isConditionSet(isConditionSetParams{
		resource:      &details.grpcRoute,
		conditions:    parentStatus.Conditions,
		conditionType: string(gatewayv1.RouteConditionResolvedRefs),
		annotations: map[string]string{
			GRPCRouteProgrammingRevisionAnnotation: GRPCRouteProgrammingRevisionValue,
		},
	})
}

func (m *grpcRouteModelImpl) setProgrammed(
	ctx context.Context,
	params setGRPCRouteProgrammedParams,
) error {
	grpcRoute := params.grpcRoute.DeepCopy()

	err := setL7RouteProgrammed(ctx, m.resourcesModel, setL7RouteProgrammedParams{
		resource:              grpcRoute,
		parentStatuses:        grpcRoute.Status.Parents,
		gatewayClass:          params.gatewayClass,
		gateway:               params.gateway,
		matchedRef:            params.matchedRef,
		programmedPolicyRules: params.programmedPolicyRules,
		programmingAnnotation: GRPCRouteProgrammingRevisionAnnotation,
		programmingRevision:   GRPCRouteProgrammingRevisionValue,
		policyRulesAnnotation: GRPCRouteProgrammedPolicyRulesAnnotation,
		finalizer:             GRPCRouteProgrammedFinalizer,
	})
	if err != nil {
		return fmt.Errorf("failed to update programmed status for GRPCRoute %s: %w", grpcRoute.Name, err)
	}

	return nil
}

type grpcRouteModelDeps struct {
	dig.In

	K8sClient      k8sClient
	RootLogger     *slog.Logger
	GatewayModel   gatewayModel
	OciLBModel     ociLoadBalancerModel
	ResourcesModel resourcesModel
}

func newGRPCRouteModel(deps grpcRouteModelDeps) *grpcRouteModelImpl {
	return &grpcRouteModelImpl{
		client:               deps.K8sClient,
		logger:               deps.RootLogger.WithGroup("grpcroute-model"),
		gatewayModel:         deps.GatewayModel,
		ociLoadBalancerModel: deps.OciLBModel,
		resourcesModel:       deps.ResourcesModel,
	}
}
