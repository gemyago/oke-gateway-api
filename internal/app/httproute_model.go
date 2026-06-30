package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	v1 "k8s.io/api/core/v1"
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

type programRouteResult struct {
	// Names of the policy rules that were programmed for this particular route
	programmedPolicyRules []string
}

type deprovisionRouteParams struct {
	gateway          gatewayv1.Gateway
	config           types.GatewayConfig
	httpRoute        gatewayv1.HTTPRoute
	matchedListeners []gatewayv1.Listener
}

type setProgrammedParams struct {
	httpRoute    gatewayv1.HTTPRoute
	gatewayClass gatewayv1.GatewayClass
	gateway      gatewayv1.Gateway
	matchedRef   gatewayv1.ParentReference

	// List of load balancer policy rules that were programmed for this route
	programmedPolicyRules []string
}

type programmedHTTPRoutePolicyRule struct {
	listenerName string
	ruleName     string
}

type l7RouteKind string

const (
	l7HTTPRouteKind l7RouteKind = "HTTPRoute"
	l7GRPCRouteKind l7RouteKind = "GRPCRoute"
)

const routeReasonConflicted gatewayv1.RouteConditionReason = "Conflicted"

type l7RouteIdentity struct {
	kind              l7RouteKind
	namespace         string
	name              string
	creationTimestamp metav1.Time
}

type l7RouteCandidate struct {
	identity   l7RouteIdentity
	parentRefs []gatewayv1.ParentReference
	hostnames  []gatewayv1.Hostname
}

type l7RouteConflictParams struct {
	gateway          gatewayv1.Gateway
	matchedListeners []gatewayv1.Listener
	current          l7RouteCandidate
	oppositeRoutes   []l7RouteCandidate
}

type checkL7RouteConflictParams struct {
	gateway               gatewayv1.Gateway
	matchedListeners      []gatewayv1.Listener
	current               l7RouteCandidate
	oppositeRouteListName string
	listOppositeRoutes    func(context.Context) ([]l7RouteCandidate, error)
}

type rejectL7RouteParams struct {
	resource       client.Object
	parentStatuses *[]gatewayv1.RouteParentStatus
	gatewayClass   gatewayv1.GatewayClass
	matchedRef     gatewayv1.ParentReference
	message        string
	routeKind      string
}

type programL7RoutePolicyParams struct {
	loadBalancerID      string
	gateway             gatewayv1.Gateway
	config              types.GatewayConfig
	routeName           string
	routeNamespace      string
	backendRefs         []gatewayv1.BackendRef
	knownBackends       map[string]v1.Service
	matchedListeners    []gatewayv1.Listener
	previousPolicyRules []programmedHTTPRoutePolicyRule
	ruleCount           int
	makeRoutingRule     func(ruleIndex int) (loadbalancer.RoutingRule, error)
	backendTLSPolicy    backendTLSPolicyModel
}

type setL7RouteProgrammedParams struct {
	resource              client.Object
	parentStatuses        []gatewayv1.RouteParentStatus
	gatewayClass          gatewayv1.GatewayClass
	gateway               gatewayv1.Gateway
	matchedRef            gatewayv1.ParentReference
	programmedPolicyRules []string
	programmingAnnotation string
	programmingRevision   string
	policyRulesAnnotation string
	finalizer             string
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
	) (programRouteResult, error)

	// deprovisionRoute deprovisions a given HTTPRoute, which includes removing any
	// associated load balancer resources, and object finalizer
	deprovisionRoute(
		ctx context.Context,
		params deprovisionRouteParams,
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

func l7RouteConflictingWinner(params l7RouteConflictParams) (l7RouteCandidate, bool) {
	for _, oppositeRoute := range params.oppositeRoutes {
		if params.current.identity.kind != oppositeRoute.identity.kind {
			continue
		}
		if !l7RoutesShareListenerHostname(params.gateway, params.matchedListeners, params.current, oppositeRoute) {
			continue
		}
		if l7RouteWins(oppositeRoute.identity, params.current.identity) {
			return oppositeRoute, true
		}
	}
	return l7RouteCandidate{}, false
}

func checkL7RouteConflict(ctx context.Context, params checkL7RouteConflictParams) (l7RouteCandidate, bool, error) {
	if len(params.matchedListeners) == 0 {
		return l7RouteCandidate{}, false, nil
	}
	oppositeRoutes, err := params.listOppositeRoutes(ctx)
	if err != nil {
		return l7RouteCandidate{}, false, fmt.Errorf(
			"failed to list %s for conflict detection: %w",
			params.oppositeRouteListName,
			err,
		)
	}
	winner, conflicted := l7RouteConflictingWinner(l7RouteConflictParams{
		gateway:          params.gateway,
		matchedListeners: params.matchedListeners,
		current:          params.current,
		oppositeRoutes:   oppositeRoutes,
	})
	return winner, conflicted, nil
}

func rejectL7Route(ctx context.Context, k8sClient k8sClient, params rejectL7RouteParams) error {
	parentStatus, parentStatusIndex, found := lo.FindIndexOf(
		*params.parentStatuses,
		func(status gatewayv1.RouteParentStatus) bool {
			return status.ControllerName == params.gatewayClass.Spec.ControllerName &&
				parentRefSameTarget(status.ParentRef, params.matchedRef)
		},
	)
	if !found {
		parentStatus = gatewayv1.RouteParentStatus{
			ParentRef:      makeTargetOnlyParentRef(params.matchedRef),
			ControllerName: params.gatewayClass.Spec.ControllerName,
		}
	}

	meta.SetStatusCondition(&parentStatus.Conditions, metav1.Condition{
		Type:               string(gatewayv1.RouteConditionAccepted),
		Status:             metav1.ConditionFalse,
		Reason:             string(routeReasonConflicted),
		ObservedGeneration: params.resource.GetGeneration(),
		LastTransitionTime: metav1.Now(),
		Message:            params.message,
	})

	if found {
		(*params.parentStatuses)[parentStatusIndex] = parentStatus
	} else {
		*params.parentStatuses = append(*params.parentStatuses, parentStatus)
	}

	if err := k8sClient.Status().Update(ctx, params.resource); err != nil {
		return fmt.Errorf(
			"failed to update rejected status for %s %s: %w",
			params.routeKind,
			params.resource.GetName(),
			err,
		)
	}
	return nil
}

func removeL7RoutePolicyRules(
	ctx context.Context,
	ociLoadBalancerModel ociLoadBalancerModel,
	loadBalancerID string,
	matchedListeners []gatewayv1.Listener,
	programmedPolicyRulesAnnotation string,
) error {
	previousRules := parseProgrammedHTTPRoutePolicyRules(programmedPolicyRulesAnnotation)
	prevRulesByListener := previousPolicyRulesByListener(previousRules, matchedListeners)
	listenerNames := lo.Keys(prevRulesByListener)
	sort.Strings(listenerNames)

	for _, listenerName := range listenerNames {
		err := ociLoadBalancerModel.commitRoutingPolicy(ctx, commitRoutingPolicyParams{
			loadBalancerID:  loadBalancerID,
			listenerName:    listenerName,
			policyRules:     []loadbalancer.RoutingRule{},
			prevPolicyRules: prevRulesByListener[listenerName],
		})
		if err != nil {
			return fmt.Errorf("failed to remove route policy rules for listener %s: %w", listenerName, err)
		}
	}

	return nil
}

func l7RoutesShareListenerHostname(
	gateway gatewayv1.Gateway,
	matchedListeners []gatewayv1.Listener,
	a l7RouteCandidate,
	b l7RouteCandidate,
) bool {
	listenerByName := lo.SliceToMap(
		gateway.Spec.Listeners,
		func(listener gatewayv1.Listener) (gatewayv1.SectionName, gatewayv1.Listener) {
			return listener.Name, listener
		},
	)
	matchedListenerNames := lo.SliceToMap(
		matchedListeners,
		func(listener gatewayv1.Listener) (gatewayv1.SectionName, struct{}) {
			return listener.Name, struct{}{}
		},
	)
	for _, listenerName := range l7RouteAttachedListenerNames(gateway, b.parentRefs, b.identity.namespace) {
		if _, matched := matchedListenerNames[listenerName]; !matched {
			continue
		}
		listener, found := listenerByName[listenerName]
		if !found {
			continue
		}
		if l7RouteHostnamesIntersect(
			l7RouteHostnamesForListener(a.hostnames, listener),
			l7RouteHostnamesForListener(b.hostnames, listener),
		) {
			return true
		}
	}
	return false
}

func l7RouteAttachedListenerNames(
	gateway gatewayv1.Gateway,
	parentRefs []gatewayv1.ParentReference,
	routeNamespace string,
) []gatewayv1.SectionName {
	listenerNames := lo.SliceToMap(
		gateway.Spec.Listeners,
		func(listener gatewayv1.Listener) (gatewayv1.SectionName, struct{}) {
			return listener.Name, struct{}{}
		},
	)
	result := make([]gatewayv1.SectionName, 0, len(gateway.Spec.Listeners))
	for _, parentRef := range parentRefs {
		parentNamespace := routeNamespace
		if parentRef.Namespace != nil {
			parentNamespace = string(*parentRef.Namespace)
		}
		if parentNamespace != gateway.Namespace || string(parentRef.Name) != gateway.Name {
			continue
		}
		if parentRef.SectionName != nil {
			if _, found := listenerNames[*parentRef.SectionName]; found {
				result = append(result, *parentRef.SectionName)
			}
			continue
		}
		for _, listener := range gateway.Spec.Listeners {
			result = append(result, listener.Name)
		}
	}
	return lo.Uniq(result)
}

func l7RouteHostnamesForListener(
	routeHostnames []gatewayv1.Hostname,
	listener gatewayv1.Listener,
) []gatewayv1.Hostname {
	if listener.Hostname == nil {
		if len(routeHostnames) == 0 {
			return []gatewayv1.Hostname{""}
		}
		return routeHostnames
	}
	if len(routeHostnames) == 0 {
		return []gatewayv1.Hostname{*listener.Hostname}
	}
	return lo.Filter(routeHostnames, func(hostname gatewayv1.Hostname, _ int) bool {
		return l7HostnamePatternsIntersect(hostname, *listener.Hostname)
	})
}

func l7RouteHostnamesIntersect(a, b []gatewayv1.Hostname) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	for _, aHostname := range a {
		for _, bHostname := range b {
			if l7HostnamePatternsIntersect(aHostname, bHostname) {
				return true
			}
		}
	}
	return false
}

func l7HostnamePatternsIntersect(a, b gatewayv1.Hostname) bool {
	aValue := strings.ToLower(string(a))
	bValue := strings.ToLower(string(b))
	if aValue == "" || bValue == "" {
		return true
	}
	if aValue == bValue {
		return true
	}
	aWildcard := strings.HasPrefix(aValue, "*.")
	bWildcard := strings.HasPrefix(bValue, "*.")
	switch {
	case aWildcard && bWildcard:
		aSuffix := strings.TrimPrefix(aValue, "*.")
		bSuffix := strings.TrimPrefix(bValue, "*.")
		return aSuffix == bSuffix ||
			strings.HasSuffix(aSuffix, "."+bSuffix) ||
			strings.HasSuffix(bSuffix, "."+aSuffix)
	case aWildcard:
		return l7WildcardHostnameMatches(aValue, bValue)
	case bWildcard:
		return l7WildcardHostnameMatches(bValue, aValue)
	default:
		return false
	}
}

func l7WildcardHostnameMatches(pattern string, hostname string) bool {
	suffix := strings.TrimPrefix(pattern, "*.")
	return hostname != suffix && strings.HasSuffix(hostname, "."+suffix)
}

func l7RouteWins(a, b l7RouteIdentity) bool {
	if !a.creationTimestamp.Equal(&b.creationTimestamp) {
		return a.creationTimestamp.Before(&b.creationTimestamp)
	}
	aName := a.namespace + "/" + a.name
	bName := b.namespace + "/" + b.name
	if aName != bName {
		return aName < bName
	}
	return a.kind < b.kind
}

func backendRefName(
	backendRef gatewayv1.HTTPBackendRef,
	defaultNamespace string,
) apitypes.NamespacedName {
	return backendObjectRefName(backendRef.BackendObjectReference, defaultNamespace)
}

func backendObjectRefName(
	backendRef gatewayv1.BackendObjectReference,
	defaultNamespace string,
) apitypes.NamespacedName {
	return apitypes.NamespacedName{
		Name: string(backendRef.Name),
		Namespace: lo.IfF(
			backendRef.Namespace != nil,
			func() string { return string(*backendRef.Namespace) },
		).Else(defaultNamespace),
	}
}

func l7BackendRefKey(backendRef gatewayv1.BackendRef, defaultNamespace string) string {
	refName := backendObjectRefName(backendRef.BackendObjectReference, defaultNamespace)
	port := lo.FromPtr(backendRef.BackendObjectReference.Port)
	return fmt.Sprintf("%s/%s:%d", refName.Namespace, refName.Name, port)
}

func parseProgrammedHTTPRoutePolicyRules(annotationValue string) []programmedHTTPRoutePolicyRule {
	if annotationValue == "" {
		return nil
	}

	entries := strings.Split(annotationValue, ",")
	rules := make([]programmedHTTPRoutePolicyRule, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		listenerName, ruleName, found := strings.Cut(entry, "/")
		if !found {
			rules = append(rules, programmedHTTPRoutePolicyRule{ruleName: entry})
			continue
		}
		if listenerName == "" || ruleName == "" {
			continue
		}

		rules = append(rules, programmedHTTPRoutePolicyRule{
			listenerName: listenerName,
			ruleName:     ruleName,
		})
	}

	return rules
}

func programmedHTTPRoutePolicyRulesAnnotation(
	listeners []gatewayv1.Listener,
	ruleNames []string,
) []string {
	rules := make([]string, 0, len(listeners)*len(ruleNames))
	for _, listener := range listeners {
		for _, ruleName := range ruleNames {
			rules = append(rules, fmt.Sprintf("%s/%s", listener.Name, ruleName))
		}
	}

	return rules
}

func previousPolicyRulesByListener(
	previousRules []programmedHTTPRoutePolicyRule,
	currentListeners []gatewayv1.Listener,
) map[string][]string {
	previousByListener := map[string][]string{}
	currentListenerNames := lo.SliceToMap(currentListeners, func(listener gatewayv1.Listener) (string, struct{}) {
		return string(listener.Name), struct{}{}
	})

	for _, previousRule := range previousRules {
		if previousRule.listenerName == "" {
			for listenerName := range currentListenerNames {
				previousByListener[listenerName] = append(previousByListener[listenerName], previousRule.ruleName)
			}
			continue
		}

		previousByListener[previousRule.listenerName] = append(
			previousByListener[previousRule.listenerName],
			previousRule.ruleName,
		)
	}

	return previousByListener
}

type httpRouteModelImpl struct {
	client               k8sClient
	logger               *slog.Logger
	gatewayModel         gatewayModel
	resourcesModel       resourcesModel
	ociLoadBalancerModel ociLoadBalancerModel
	backendTLSPolicy     backendTLSPolicyModel
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
		matchingListeners := lo.Filter(
			resolvedGatewayData.gateway.Spec.Listeners,
			func(l gatewayv1.Listener, _ int) bool {
				return l.Name == sectionName
			},
		)

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
	winner, conflicted, err := checkL7RouteConflict(ctx, checkL7RouteConflictParams{
		gateway:          routeDetails.gatewayDetails.gateway,
		matchedListeners: routeDetails.matchedListeners,
		current: l7RouteCandidate{
			identity: l7RouteIdentity{
				kind:              l7HTTPRouteKind,
				namespace:         routeDetails.httpRoute.Namespace,
				name:              routeDetails.httpRoute.Name,
				creationTimestamp: routeDetails.httpRoute.CreationTimestamp,
			},
			parentRefs: routeDetails.httpRoute.Spec.ParentRefs,
			hostnames:  routeDetails.httpRoute.Spec.Hostnames,
		},
		oppositeRouteListName: "GRPCRoutes",
		listOppositeRoutes:    m.listGRPCRouteConflictCandidates,
	})
	if err != nil {
		return nil, err
	}
	if conflicted {
		return nil, m.rejectRoute(ctx, routeDetails, l7RouteConflictMessage(winner))
	}

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

	if updateErr := m.client.Status().Update(ctx, httpRoute); updateErr != nil {
		return nil, fmt.Errorf("failed to update status for HTTProute %s: %w", httpRoute.Name, updateErr)
	}

	return httpRoute, nil
}

func (m *httpRouteModelImpl) listGRPCRouteConflictCandidates(ctx context.Context) ([]l7RouteCandidate, error) {
	var grpcRoutes gatewayv1.GRPCRouteList
	if err := m.client.List(ctx, &grpcRoutes); err != nil {
		return nil, err
	}
	return lo.FilterMap(grpcRoutes.Items, func(route gatewayv1.GRPCRoute, _ int) (l7RouteCandidate, bool) {
		if route.DeletionTimestamp != nil {
			return l7RouteCandidate{}, false
		}
		return l7RouteCandidate{
			identity: l7RouteIdentity{
				kind:              l7GRPCRouteKind,
				namespace:         route.Namespace,
				name:              route.Name,
				creationTimestamp: route.CreationTimestamp,
			},
			parentRefs: route.Spec.ParentRefs,
			hostnames:  route.Spec.Hostnames,
		}, true
	}), nil
}

func l7RouteConflictMessage(winner l7RouteCandidate) string {
	return fmt.Sprintf(
		"Route conflicts with %s %s/%s on an overlapping listener hostname",
		winner.identity.kind,
		winner.identity.namespace,
		winner.identity.name,
	)
}

func (m *httpRouteModelImpl) rejectRoute(
	ctx context.Context,
	routeDetails resolvedRouteDetails,
	message string,
) error {
	httpRoute := routeDetails.httpRoute.DeepCopy()
	if programmedPolicyRulesAnnotation, ok := httpRoute.Annotations[HTTPRouteProgrammedPolicyRulesAnnotation]; ok {
		if err := removeL7RoutePolicyRules(
			ctx,
			m.ociLoadBalancerModel,
			routeDetails.gatewayDetails.config.Spec.LoadBalancerID,
			routeDetails.matchedListeners,
			programmedPolicyRulesAnnotation,
		); err != nil {
			return fmt.Errorf("failed to remove rejected HTTPRoute policy rules: %w", err)
		}
	}

	return rejectL7Route(ctx, m.client, rejectL7RouteParams{
		resource:       httpRoute,
		parentStatuses: &httpRoute.Status.Parents,
		gatewayClass:   routeDetails.gatewayDetails.gatewayClass,
		matchedRef:     routeDetails.matchedRef,
		message:        message,
		routeKind:      "HTTPRoute",
	})
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

func programL7RoutePolicy(
	ctx context.Context,
	ociLoadBalancerModel ociLoadBalancerModel,
	params programL7RoutePolicyParams,
) ([]string, error) {
	processedBackendRefs := make(map[string]struct{})
	for _, backendRef := range params.backendRefs {
		key := l7BackendRefKey(backendRef, params.routeNamespace)
		if _, ok := processedBackendRefs[key]; ok {
			continue
		}
		serviceName := backendObjectRefName(backendRef.BackendObjectReference, params.routeNamespace).String()
		service, ok := params.knownBackends[serviceName]
		if !ok {
			return nil, fmt.Errorf("resolved backend service %s not found", serviceName)
		}
		backendSSLConfig, manageSSLConfig, err := resolveL7BackendSSLConfig(ctx, params, service, backendRef)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve BackendTLSPolicy for service %s: %w", key, err)
		}
		err = ociLoadBalancerModel.reconcileBackendSet(ctx, reconcileBackendSetParams{
			loadBalancerID:  params.loadBalancerID,
			service:         service,
			routeNS:         params.routeNamespace,
			backendRef:      backendRef,
			sslConfig:       backendSSLConfig,
			manageSSLConfig: manageSSLConfig,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to reconcile backend set for service %s: %w", key, err)
		}
		processedBackendRefs[key] = struct{}{}
	}

	policyRules := make([]loadbalancer.RoutingRule, 0, params.ruleCount)
	policyRuleNames := make([]string, 0, params.ruleCount)
	for ruleIndex := range params.ruleCount {
		rule, err := params.makeRoutingRule(ruleIndex)
		if err != nil {
			return nil, fmt.Errorf("failed to make routing rule %d for route %s: %w", ruleIndex, params.routeName, err)
		}
		policyRules = append(policyRules, rule)
		policyRuleNames = append(policyRuleNames, *rule.Name)
	}

	prevRulesByListener := previousPolicyRulesByListener(params.previousPolicyRules, params.matchedListeners)
	currentListenerNames := lo.SliceToMap(
		params.matchedListeners,
		func(listener gatewayv1.Listener) (string, struct{}) {
			return string(listener.Name), struct{}{}
		},
	)

	for _, listener := range params.matchedListeners {
		listenerName := string(listener.Name)
		err := ociLoadBalancerModel.commitRoutingPolicy(ctx, commitRoutingPolicyParams{
			loadBalancerID:  params.loadBalancerID,
			listenerName:    listenerName,
			policyRules:     policyRules,
			prevPolicyRules: prevRulesByListener[listenerName],
		})
		if err != nil {
			return nil, fmt.Errorf("failed to commit routing policy for listener %s: %w", listener.Name, err)
		}
	}

	staleListenerNames := lo.Keys(prevRulesByListener)
	sort.Strings(staleListenerNames)
	for _, listenerName := range staleListenerNames {
		if _, ok := currentListenerNames[listenerName]; ok {
			continue
		}
		err := ociLoadBalancerModel.commitRoutingPolicy(ctx, commitRoutingPolicyParams{
			loadBalancerID:  params.loadBalancerID,
			listenerName:    listenerName,
			policyRules:     []loadbalancer.RoutingRule{},
			prevPolicyRules: prevRulesByListener[listenerName],
		})
		if err != nil {
			return nil, fmt.Errorf("failed to remove stale routing policy rules for listener %s: %w", listenerName, err)
		}
	}

	return programmedHTTPRoutePolicyRulesAnnotation(params.matchedListeners, policyRuleNames), nil
}

func resolveL7BackendSSLConfig(
	ctx context.Context,
	params programL7RoutePolicyParams,
	service v1.Service,
	backendRef gatewayv1.BackendRef,
) (*loadbalancer.SslConfigurationDetails, bool, error) {
	if params.backendTLSPolicy == nil {
		return nil, false, nil
	}
	sslConfig, err := params.backendTLSPolicy.resolveForBackendRef(ctx, resolveBackendTLSPolicyParams{
		gateway:    params.gateway,
		config:     params.config,
		service:    service,
		backendRef: backendRef,
	})
	if errors.Is(err, errBackendTLSPolicyNotFound) {
		return nil, false, nil
	}
	return sslConfig, true, err
}

func (m *httpRouteModelImpl) programRoute(
	ctx context.Context,
	params programRouteParams,
) (programRouteResult, error) {
	var previousRules []programmedHTTPRoutePolicyRule
	if prevPolicyRulesStr, ok := params.httpRoute.Annotations[HTTPRouteProgrammedPolicyRulesAnnotation]; ok {
		previousRules = parseProgrammedHTTPRoutePolicyRules(prevPolicyRulesStr)
	}

	programmedPolicyRules, err := programL7RoutePolicy(ctx, m.ociLoadBalancerModel, programL7RoutePolicyParams{
		loadBalancerID:      params.config.Spec.LoadBalancerID,
		gateway:             params.gateway,
		config:              params.config,
		routeName:           params.httpRoute.Name,
		routeNamespace:      params.httpRoute.Namespace,
		backendRefs:         httpRouteBackendRefs(params.httpRoute),
		knownBackends:       params.knownBackends,
		matchedListeners:    params.matchedListeners,
		previousPolicyRules: previousRules,
		backendTLSPolicy:    m.backendTLSPolicy,
		ruleCount:           len(params.httpRoute.Spec.Rules),
		makeRoutingRule: func(ruleIndex int) (loadbalancer.RoutingRule, error) {
			return m.ociLoadBalancerModel.makeRoutingRule(ctx, makeRoutingRuleParams{
				httpRoute:          params.httpRoute,
				httpRouteRuleIndex: ruleIndex,
			})
		},
	})
	if err != nil {
		return programRouteResult{}, err
	}

	return programRouteResult{
		programmedPolicyRules: programmedPolicyRules,
	}, nil
}

func httpRouteBackendRefs(route gatewayv1.HTTPRoute) []gatewayv1.BackendRef {
	backendRefs := make([]gatewayv1.BackendRef, 0)
	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			backendRefs = append(backendRefs, backendRef.BackendRef)
		}
	}
	return backendRefs
}

func (m *httpRouteModelImpl) deprovisionRoute(
	ctx context.Context,
	params deprovisionRouteParams,
) error {
	var previousRules []programmedHTTPRoutePolicyRule
	if prevPolicyRulesStr, ok := params.httpRoute.Annotations[HTTPRouteProgrammedPolicyRulesAnnotation]; ok {
		previousRules = parseProgrammedHTTPRoutePolicyRules(prevPolicyRulesStr)
	}

	if len(previousRules) == 0 {
		m.logger.InfoContext(ctx, "No previous policy rules found in annotation, skipping deprovisioning.",
			slog.String("route", params.httpRoute.Name),
			slog.String("annotationKey", HTTPRouteProgrammedPolicyRulesAnnotation),
		)
		return nil
	}

	prevRulesByListener := previousPolicyRulesByListener(previousRules, params.matchedListeners)
	listenerNames := lo.Keys(prevRulesByListener)
	sort.Strings(listenerNames)

	for _, listenerName := range listenerNames {
		m.logger.DebugContext(ctx, "Deprovisioning listener policies for HTTPRoute",
			slog.String("route", params.httpRoute.Name),
			slog.String("listener", listenerName),
			slog.String("loadBalancerID", params.config.Spec.LoadBalancerID),
			slog.Any("prevPolicyRules", prevRulesByListener[listenerName]),
		)
		err := m.ociLoadBalancerModel.commitRoutingPolicy(ctx, commitRoutingPolicyParams{
			loadBalancerID:  params.config.Spec.LoadBalancerID,
			listenerName:    listenerName,
			policyRules:     []loadbalancer.RoutingRule{}, // Empty rules for deprovisioning
			prevPolicyRules: prevRulesByListener[listenerName],
		})
		if err != nil {
			return fmt.Errorf("failed to deprovision routing policy for listener %s: %w", listenerName, err)
		}
	}

	// TODO: Dedup and filter-out non service refs
	for _, rule := range params.httpRoute.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			err := m.ociLoadBalancerModel.deprovisionBackendSet(ctx, deprovisionBackendSetParams{
				loadBalancerID: params.config.Spec.LoadBalancerID,
				routeNamespace: params.httpRoute.Namespace,
				backendRef:     backendRef.BackendRef,
			})
			if err != nil {
				return fmt.Errorf(
					"failed to deprovision backend set for rule %s/%s: %w",
					params.httpRoute.Namespace,
					params.httpRoute.Name,
					err,
				)
			}
		}
	}

	routeToUpdate := params.httpRoute.DeepCopy()
	controllerutil.RemoveFinalizer(routeToUpdate, HTTPRouteProgrammedFinalizer)

	if err := m.client.Update(ctx, routeToUpdate); err != nil {
		return fmt.Errorf("failed to update HTTPRoute %s/%s after deprovisioning: %w",
			routeToUpdate.Namespace, routeToUpdate.Name, err)
	}

	return nil
}

//nolint:unparam // The error return is part of the interface contract used by controller tests.
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
		resource:   &details.httpRoute,
		conditions: parentStatus.Conditions,

		// This is probably not the best condition type for programmed status
		// but the spec doesn't define a "programmed" condition for route for some reason
		// so using resolved refs, and we may want to add a custom condition
		// This condition is also used by indexer to check if the route is programmed
		// so update watches model as well
		conditionType: string(gatewayv1.RouteConditionResolvedRefs),

		annotations: map[string]string{
			HTTPRouteProgrammingRevisionAnnotation: HTTPRouteProgrammingRevisionValue,
		},
	}), nil
}

func setL7RouteProgrammed(
	ctx context.Context,
	resourcesModel resourcesModel,
	params setL7RouteProgrammedParams,
) error {
	_, statusIndex, found := lo.FindIndexOf(
		params.parentStatuses,
		func(status gatewayv1.RouteParentStatus) bool {
			return status.ControllerName == params.gatewayClass.Spec.ControllerName &&
				parentRefSameTarget(status.ParentRef, params.matchedRef)
		},
	)
	if !found {
		return fmt.Errorf("parent status not found for controller %s and parentRef %s",
			params.gatewayClass.Spec.ControllerName,
			params.matchedRef.Name,
		)
	}

	return resourcesModel.setCondition(ctx, setConditionParams{
		resource:      params.resource,
		conditions:    &params.parentStatuses[statusIndex].Conditions,
		conditionType: string(gatewayv1.RouteConditionResolvedRefs),
		status:        metav1.ConditionTrue,
		reason:        string(gatewayv1.RouteReasonResolvedRefs),
		message:       fmt.Sprintf("Route programmed by %s", params.gateway.Name),
		annotations: map[string]string{
			params.programmingAnnotation: params.programmingRevision,
			params.policyRulesAnnotation: strings.Join(params.programmedPolicyRules, ","),
		},
		finalizer: params.finalizer,
	})
}

func (m *httpRouteModelImpl) setProgrammed(
	ctx context.Context,
	params setProgrammedParams,
) error {
	httpRoute := params.httpRoute.DeepCopy()

	err := setL7RouteProgrammed(ctx, m.resourcesModel, setL7RouteProgrammedParams{
		resource:              httpRoute,
		parentStatuses:        httpRoute.Status.Parents,
		gatewayClass:          params.gatewayClass,
		gateway:               params.gateway,
		matchedRef:            params.matchedRef,
		programmedPolicyRules: params.programmedPolicyRules,
		programmingAnnotation: HTTPRouteProgrammingRevisionAnnotation,
		programmingRevision:   HTTPRouteProgrammingRevisionValue,
		policyRulesAnnotation: HTTPRouteProgrammedPolicyRulesAnnotation,
		finalizer:             HTTPRouteProgrammedFinalizer,
	})
	if err != nil {
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
	BackendTLS     backendTLSPolicyModel
}

// newHTTPRouteModel creates a new instance of httpRouteModel.
func newHTTPRouteModel(deps httpRouteModelDeps) *httpRouteModelImpl {
	return &httpRouteModelImpl{
		client:               deps.K8sClient,
		logger:               deps.RootLogger.WithGroup("httproute-model"),
		gatewayModel:         deps.GatewayModel,
		ociLoadBalancerModel: deps.OciLBModel,
		resourcesModel:       deps.ResourcesModel,
		backendTLSPolicy:     deps.BackendTLS,
	}
}
