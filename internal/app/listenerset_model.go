package app

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	apitypes "k8s.io/apimachinery/pkg/types"
	v1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
)

const maxOCIListenerNameLength = 32
const ListenerSetParentGatewayAnnotation = "oke-gateway-api.gemyago.github.io/listenerset-parent-gateway"

type effectiveListenerSourceKind string

const (
	effectiveListenerSourceGateway     effectiveListenerSourceKind = "Gateway"
	effectiveListenerSourceListenerSet effectiveListenerSourceKind = "ListenerSet"
)

type effectiveListener struct {
	listener           v1.Listener
	sourceKind         effectiveListenerSourceKind
	sourceNamespace    string
	sourceName         string
	ociName            string
	conflicted         bool
	conflictReason     v1.ListenerConditionReason
	unsupported        bool
	unsupportedReason  v1.ListenerConditionReason
	unsupportedMessage string
}

func effectiveListenerObjectKey(listener effectiveListener) string {
	return path.Join(listener.sourceNamespace, listener.sourceName, string(listener.listener.Name))
}

func effectiveListenerOCIListener(listener effectiveListener) v1.Listener {
	ociListener := listener.listener
	ociListener.Name = v1.SectionName(listener.ociName)
	if listener.sourceKind == effectiveListenerSourceListenerSet && ociListener.TLS != nil {
		ociListener.TLS = ociListener.TLS.DeepCopy()
		for i := range ociListener.TLS.CertificateRefs {
			if ociListener.TLS.CertificateRefs[i].Namespace == nil {
				namespace := v1.Namespace(listener.sourceNamespace)
				ociListener.TLS.CertificateRefs[i].Namespace = &namespace
			}
		}
	}
	return ociListener
}

func listenerSetParentRefTargetsGateway(parentRef v1.ParentGatewayReference) bool {
	if parentRef.Group != nil && string(*parentRef.Group) != v1.GroupName {
		return false
	}
	if parentRef.Kind != nil && string(*parentRef.Kind) != "Gateway" {
		return false
	}
	return true
}

func listenerSetParentGatewayName(listenerSet v1.ListenerSet) (string, bool) {
	parentRef := listenerSet.Spec.ParentRef
	if !listenerSetParentRefTargetsGateway(parentRef) {
		return "", false
	}
	namespace := listenerSet.Namespace
	if parentRef.Namespace != nil {
		namespace = string(*parentRef.Namespace)
	}
	return path.Join(namespace, string(parentRef.Name)), true
}

func listenerSetParentGatewayTarget(
	ctx context.Context,
	k8sClient k8sClient,
	routeNamespace string,
	parentRef v1.ParentReference,
) (apitypes.NamespacedName, bool, error) {
	parentName := parentRefTargetName(parentRef, routeNamespace)
	var listenerSet v1.ListenerSet
	if err := k8sClient.Get(ctx, parentName, &listenerSet); err != nil {
		if apierrors.IsNotFound(err) {
			return apitypes.NamespacedName{}, false, nil
		}
		return apitypes.NamespacedName{}, false, fmt.Errorf(
			"failed to get ListenerSet %s: %w",
			parentName.String(),
			err,
		)
	}
	parentGatewayName, ok := listenerSetParentGatewayName(listenerSet)
	if !ok {
		return apitypes.NamespacedName{}, false, nil
	}
	namespace, name, ok := strings.Cut(parentGatewayName, "/")
	if !ok {
		return apitypes.NamespacedName{}, false, nil
	}
	return apitypes.NamespacedName{Namespace: namespace, Name: name}, true, nil
}

func listenerSetAllowedByGateway(
	gateway v1.Gateway,
	listenerSet v1.ListenerSet,
	listenerSetNamespace corev1.Namespace,
) bool {
	if gateway.Spec.AllowedListeners == nil || gateway.Spec.AllowedListeners.Namespaces == nil {
		return false
	}

	namespaces := gateway.Spec.AllowedListeners.Namespaces
	from := v1.NamespacesFromNone
	if namespaces.From != nil {
		from = *namespaces.From
	}

	switch from {
	case v1.NamespacesFromAll:
		return true
	case v1.NamespacesFromSame:
		return listenerSet.Namespace == gateway.Namespace
	case v1.NamespacesFromSelector:
		if namespaces.Selector == nil {
			return false
		}
		selector, err := metav1.LabelSelectorAsSelector(namespaces.Selector)
		if err != nil {
			return false
		}
		return selector.Matches(labels.Set(listenerSetNamespace.Labels))
	case v1.NamespacesFromNone:
		return false
	default:
		return false
	}
}

func effectiveListenersForGateway(gateway v1.Gateway, listenerSets []v1.ListenerSet) []effectiveListener {
	result := make([]effectiveListener, 0, len(gateway.Spec.Listeners)+len(listenerSets))
	for _, listener := range gateway.Spec.Listeners {
		result = append(result, effectiveListener{
			listener:        listener,
			sourceKind:      effectiveListenerSourceGateway,
			sourceNamespace: gateway.Namespace,
			sourceName:      gateway.Name,
			ociName:         string(listener.Name),
		})
	}

	sort.SliceStable(listenerSets, func(i, j int) bool {
		left := listenerSets[i]
		right := listenerSets[j]
		if !left.CreationTimestamp.Equal(&right.CreationTimestamp) {
			return left.CreationTimestamp.Before(&right.CreationTimestamp)
		}
		return path.Join(left.Namespace, left.Name) < path.Join(right.Namespace, right.Name)
	})

	for _, listenerSet := range listenerSets {
		for _, entry := range listenerSet.Spec.Listeners {
			listener := listenerFromListenerSetEntry(entry)
			result = append(result, effectiveListener{
				listener:        listener,
				sourceKind:      effectiveListenerSourceListenerSet,
				sourceNamespace: listenerSet.Namespace,
				sourceName:      listenerSet.Name,
				ociName:         listenerSetOCIListenerName(gateway, listenerSet, listener),
			})
		}
	}

	markConflictedEffectiveListeners(result)
	return result
}

func effectiveOCIListenersForGateway(data *resolvedGatewayDetails) []v1.Listener {
	effectiveListeners := data.effectiveListeners
	if len(effectiveListeners) == 0 {
		effectiveListeners = effectiveListenersForGateway(data.gateway, nil)
	}
	listeners := make([]v1.Listener, 0, len(effectiveListeners))
	for _, listener := range effectiveListeners {
		if listener.conflicted || listener.unsupported {
			continue
		}
		listeners = append(listeners, effectiveListenerOCIListener(listener))
	}
	return listeners
}

func gatewayManagedOCIListenersForLoadBalancer(data *resolvedGatewayDetails) []v1.Listener {
	return lo.Filter(effectiveOCIListenersForGateway(data), func(listener v1.Listener, _ int) bool {
		return listener.Protocol != v1.TLSProtocolType
	})
}

func gatewayManagedOCIListenersForNetworkLoadBalancer(data *resolvedGatewayDetails) []v1.Listener {
	return lo.Filter(effectiveOCIListenersForGateway(data), func(listener v1.Listener, _ int) bool {
		return listener.Protocol == v1.TCPProtocolType || listener.Protocol == v1.UDPProtocolType
	})
}

func markUnsupportedListenerSetListeners(
	listeners []effectiveListener,
	controllerName v1.GatewayController,
) {
	for i := range listeners {
		if listeners[i].sourceKind != effectiveListenerSourceListenerSet || listeners[i].conflicted {
			continue
		}
		reason, message, unsupported := listenerSetListenerUnsupported(
			listeners[i].listener,
			controllerName,
		)
		if unsupported {
			listeners[i].unsupported = true
			listeners[i].unsupportedReason = reason
			listeners[i].unsupportedMessage = message
		}
	}
}

func listenerSetListenerUnsupported(
	listener v1.Listener,
	controllerName v1.GatewayController,
) (v1.ListenerConditionReason, string, bool) {
	switch controllerName {
	case v1.GatewayController(ControllerClassName):
		return loadBalancerListenerSetListenerUnsupported(listener)
	case v1.GatewayController(NetworkLoadBalancerControllerClassName):
		return networkLoadBalancerListenerSetListenerUnsupported(listener)
	default:
		return "", "", false
	}
}

func loadBalancerListenerSetListenerUnsupported(listener v1.Listener) (v1.ListenerConditionReason, string, bool) {
	switch listener.Protocol {
	case v1.HTTPProtocolType, v1.HTTPSProtocolType:
		return "", "", false
	case v1.TLSProtocolType:
		if lo.FromPtr(listener.TLS).Mode != nil && *listener.TLS.Mode == v1.TLSModeTerminate {
			return "", "", false
		}
		return v1.ListenerReasonUnsupportedValue,
			fmt.Sprintf("listener %s uses unsupported TLS mode for OCI Load Balancer", listener.Name),
			true
	case v1.TCPProtocolType, v1.UDPProtocolType:
		return v1.ListenerReasonUnsupportedProtocol,
			fmt.Sprintf(
				"listener %s uses unsupported protocol %s for OCI Load Balancer",
				listener.Name,
				listener.Protocol,
			),
			true
	default:
		return v1.ListenerReasonUnsupportedProtocol,
			fmt.Sprintf(
				"listener %s uses unsupported protocol %s for OCI Load Balancer",
				listener.Name,
				listener.Protocol,
			),
			true
	}
}

func networkLoadBalancerListenerSetListenerUnsupported(
	listener v1.Listener,
) (v1.ListenerConditionReason, string, bool) {
	switch listener.Protocol {
	case v1.TCPProtocolType, v1.UDPProtocolType:
		return "", "", false
	case v1.TLSProtocolType:
		if lo.FromPtr(listener.TLS).Mode != nil && *listener.TLS.Mode == v1.TLSModePassthrough {
			return "", "", false
		}
		return v1.ListenerReasonUnsupportedValue,
			fmt.Sprintf("listener %s uses unsupported TLS mode for OCI Network Load Balancer", listener.Name),
			true
	case v1.HTTPProtocolType, v1.HTTPSProtocolType:
		return v1.ListenerReasonUnsupportedProtocol,
			fmt.Sprintf(
				"listener %s uses unsupported protocol %s for OCI Network Load Balancer",
				listener.Name,
				listener.Protocol,
			),
			true
	default:
		return v1.ListenerReasonUnsupportedProtocol,
			fmt.Sprintf(
				"listener %s uses unsupported protocol %s for OCI Network Load Balancer",
				listener.Name,
				listener.Protocol,
			),
			true
	}
}

func effectiveListenersForParentRef(
	gatewayDetails resolvedGatewayDetails,
	parentRef v1.ParentReference,
	routeNamespace string,
	matchesListener func(v1.ParentReference, v1.Listener) bool,
) []v1.Listener {
	effectiveListeners := gatewayDetails.effectiveListeners
	if len(effectiveListeners) == 0 {
		effectiveListeners = effectiveListenersForGateway(gatewayDetails.gateway, nil)
	}

	results := make([]v1.Listener, 0)
	for _, listener := range effectiveListeners {
		if listener.conflicted {
			continue
		}
		if !parentRefTargetsEffectiveListener(parentRef, routeNamespace, gatewayDetails.gateway, listener) {
			continue
		}
		if matchesListener(parentRef, listener.listener) {
			results = append(results, effectiveListenerOCIListener(listener))
		}
	}
	return results
}

func parentRefTargetsEffectiveListener(
	parentRef v1.ParentReference,
	routeNamespace string,
	gateway v1.Gateway,
	listener effectiveListener,
) bool {
	switch {
	case parentRefTargetsGateway(parentRef):
		target := parentRefTargetName(parentRef, routeNamespace)
		return listener.sourceKind == effectiveListenerSourceGateway &&
			target.Namespace == gateway.Namespace &&
			target.Name == gateway.Name
	case parentRefTargetsListenerSet(parentRef):
		target := parentRefTargetName(parentRef, routeNamespace)
		return listener.sourceKind == effectiveListenerSourceListenerSet &&
			target.Namespace == listener.sourceNamespace &&
			target.Name == listener.sourceName
	default:
		return false
	}
}

func listenerFromListenerSetEntry(entry v1.ListenerEntry) v1.Listener {
	return v1.Listener(entry)
}

func listenerSetOCIListenerName(gateway v1.Gateway, listenerSet v1.ListenerSet, listener v1.Listener) string {
	return ociapi.ConstructOCIResourceName(
		fmt.Sprintf(
			"ls_%s",
			path.Join(
				gateway.Namespace,
				gateway.Name,
				listenerSet.Namespace,
				listenerSet.Name,
				string(listener.Name),
			),
		),
		ociapi.OCIResourceNameConfig{
			MaxLength:           maxOCIListenerNameLength,
			InvalidCharsPattern: invalidCharsForPolicyNamePattern,
		},
	)
}

func markConflictedEffectiveListeners(listeners []effectiveListener) {
	for i := range listeners {
		markEffectiveListenerConflict(&listeners[i], listeners[:i])
	}
}

func markEffectiveListenerConflict(listener *effectiveListener, previousListeners []effectiveListener) {
	if listener.conflicted {
		return
	}
	for _, previous := range previousListeners {
		reason, conflicted := effectiveListenersConflict(previous.listener, listener.listener)
		if conflicted {
			listener.conflicted = true
			listener.conflictReason = reason
			return
		}
	}
}

func effectiveListenersConflict(left v1.Listener, right v1.Listener) (v1.ListenerConditionReason, bool) {
	if left.Port != right.Port {
		return "", false
	}
	if left.Protocol != right.Protocol {
		return v1.ListenerReasonProtocolConflict, true
	}
	if listenersHaveHostnameConflict(left, right) {
		return v1.ListenerReasonHostnameConflict, true
	}
	return "", false
}

func listenersHaveHostnameConflict(left v1.Listener, right v1.Listener) bool {
	leftHostname := ""
	if left.Hostname != nil {
		leftHostname = string(*left.Hostname)
	}
	rightHostname := ""
	if right.Hostname != nil {
		rightHostname = string(*right.Hostname)
	}
	return l7HostnamePatternsIntersect(v1.Hostname(leftHostname), v1.Hostname(rightHostname))
}

func listenerSetStatusForGateway(
	gateway v1.Gateway,
	listenerSet v1.ListenerSet,
	effectiveListeners []effectiveListener,
	controllerName v1.GatewayController,
) v1.ListenerSetStatus {
	listenersByKey := make(map[string]effectiveListener, len(effectiveListeners))
	for _, listener := range effectiveListeners {
		listenersByKey[effectiveListenerObjectKey(listener)] = listener
	}

	status := listenerSetTopLevelStatusForGateway(gateway, listenerSet, listenersByKey, controllerName)
	status.Listeners = make([]v1.ListenerEntryStatus, 0, len(listenerSet.Spec.Listeners))
	for _, listener := range listenerSet.Spec.Listeners {
		effective := listenersByKey[path.Join(listenerSet.Namespace, listenerSet.Name, string(listener.Name))]
		entryStatus := v1.ListenerEntryStatus{
			Name:           listener.Name,
			SupportedKinds: supportedRouteKindsForListener(listenerFromListenerSetEntry(listener)),
		}
		if effective.conflicted || effective.unsupported {
			reason := effective.conflictReason
			message := fmt.Sprintf(
				"listener %s conflicts with an earlier Gateway or ListenerSet listener",
				listener.Name,
			)
			if effective.unsupported {
				reason = effective.unsupportedReason
				message = effective.unsupportedMessage
			}
			setListenerEntryCondition(
				&entryStatus,
				v1.ListenerConditionAccepted,
				metav1.ConditionFalse,
				reason,
				listenerSet.Generation,
				message,
			)
			setListenerEntryCondition(
				&entryStatus,
				v1.ListenerConditionProgrammed,
				metav1.ConditionFalse,
				reason,
				listenerSet.Generation,
				message,
			)
		} else {
			setListenerEntryCondition(
				&entryStatus,
				v1.ListenerConditionAccepted,
				metav1.ConditionTrue,
				v1.ListenerReasonAccepted,
				listenerSet.Generation,
				fmt.Sprintf("listener %s accepted", listener.Name),
			)
			setListenerEntryCondition(
				&entryStatus,
				v1.ListenerConditionProgrammed,
				metav1.ConditionTrue,
				v1.ListenerReasonProgrammed,
				listenerSet.Generation,
				fmt.Sprintf("listener %s programmed", listener.Name),
			)
		}
		status.Listeners = append(status.Listeners, entryStatus)
	}
	return status
}

func listenerSetTopLevelStatusForGateway(
	gateway v1.Gateway,
	listenerSet v1.ListenerSet,
	listenersByKey map[string]effectiveListener,
	controllerName v1.GatewayController,
) v1.ListenerSetStatus {
	acceptedStatus := metav1.ConditionTrue
	acceptedReason := string(v1.ListenerSetReasonAccepted)
	acceptedMessage := fmt.Sprintf(
		"ListenerSet %s/%s accepted by Gateway %s/%s",
		listenerSet.Namespace,
		listenerSet.Name,
		gateway.Namespace,
		gateway.Name,
	)
	programmedStatus := metav1.ConditionTrue
	programmedReason := string(v1.ListenerSetReasonProgrammed)
	programmedMessage := fmt.Sprintf(
		"ListenerSet %s/%s programmed by %s",
		listenerSet.Namespace,
		listenerSet.Name,
		controllerName,
	)
	if !listenerSetListenersValid(listenerSet, listenersByKey) {
		acceptedStatus = metav1.ConditionFalse
		acceptedReason = string(v1.ListenerSetReasonListenersNotValid)
		acceptedMessage = fmt.Sprintf(
			"ListenerSet %s/%s has invalid listeners",
			listenerSet.Namespace,
			listenerSet.Name,
		)
		programmedStatus = metav1.ConditionFalse
		programmedReason = string(v1.ListenerSetReasonListenersNotValid)
		programmedMessage = acceptedMessage
	}

	status := v1.ListenerSetStatus{}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               string(v1.ListenerSetConditionAccepted),
		Status:             acceptedStatus,
		Reason:             acceptedReason,
		ObservedGeneration: listenerSet.Generation,
		LastTransitionTime: metav1.Now(),
		Message:            acceptedMessage,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               string(v1.ListenerSetConditionProgrammed),
		Status:             programmedStatus,
		Reason:             programmedReason,
		ObservedGeneration: listenerSet.Generation,
		LastTransitionTime: metav1.Now(),
		Message:            programmedMessage,
	})
	return status
}

func listenerSetListenersValid(listenerSet v1.ListenerSet, listenersByKey map[string]effectiveListener) bool {
	for _, listener := range listenerSet.Spec.Listeners {
		effective := listenersByKey[path.Join(listenerSet.Namespace, listenerSet.Name, string(listener.Name))]
		if effective.conflicted || effective.unsupported {
			return false
		}
	}
	return true
}

func setListenerEntryCondition(
	status *v1.ListenerEntryStatus,
	conditionType v1.ListenerConditionType,
	conditionStatus metav1.ConditionStatus,
	reason v1.ListenerConditionReason,
	generation int64,
	message string,
) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               string(conditionType),
		Status:             conditionStatus,
		Reason:             string(reason),
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
		Message:            message,
	})
}

func supportedRouteKindsForListener(listener v1.Listener) []v1.RouteGroupKind {
	kindNames := supportedRouteKindNamesForProtocol(listener.Protocol)
	if listener.AllowedRoutes != nil && len(listener.AllowedRoutes.Kinds) > 0 {
		allowedKinds := make(map[v1.Kind]struct{}, len(listener.AllowedRoutes.Kinds))
		for _, allowedKind := range listener.AllowedRoutes.Kinds {
			group := gatewayAPIGroup
			if allowedKind.Group != nil {
				group = string(*allowedKind.Group)
			}
			if group == gatewayAPIGroup {
				allowedKinds[allowedKind.Kind] = struct{}{}
			}
		}
		kindNames = lo.Filter(kindNames, func(kind v1.Kind, _ int) bool {
			_, allowed := allowedKinds[kind]
			return allowed
		})
	}
	result := make([]v1.RouteGroupKind, 0, len(kindNames))
	group := v1.Group(v1.GroupName)
	for _, kind := range kindNames {
		result = append(result, v1.RouteGroupKind{Group: &group, Kind: kind})
	}
	return result
}

func supportedRouteKindNamesForProtocol(protocol v1.ProtocolType) []v1.Kind {
	switch protocol {
	case v1.HTTPProtocolType, v1.HTTPSProtocolType:
		return []v1.Kind{"HTTPRoute", "GRPCRoute"}
	case v1.TLSProtocolType:
		return []v1.Kind{"TLSRoute"}
	case v1.TCPProtocolType:
		return []v1.Kind{"TCPRoute"}
	case v1.UDPProtocolType:
		return []v1.Kind{"UDPRoute"}
	default:
		return nil
	}
}

func listenerSetStatusSemanticallyEqual(left v1.ListenerSetStatus, right v1.ListenerSetStatus) bool {
	if !conditionsSemanticallyEqual(left.Conditions, right.Conditions) || len(left.Listeners) != len(right.Listeners) {
		return false
	}
	leftListeners := lo.SliceToMap(
		left.Listeners,
		func(listener v1.ListenerEntryStatus) (v1.SectionName, v1.ListenerEntryStatus) {
			return listener.Name, listener
		},
	)
	for _, rightListener := range right.Listeners {
		leftListener, found := leftListeners[rightListener.Name]
		if !found ||
			leftListener.AttachedRoutes != rightListener.AttachedRoutes ||
			!routeGroupKindsEqual(leftListener.SupportedKinds, rightListener.SupportedKinds) ||
			!conditionsSemanticallyEqual(leftListener.Conditions, rightListener.Conditions) {
			return false
		}
	}
	return true
}

func attachedListenerSetCount(listenerSets []v1.ListenerSet) *int32 {
	const maxInt32 = int32(1<<31 - 1)

	var count int32
	for range listenerSets {
		if count == maxInt32 {
			break
		}
		count++
	}
	return &count
}

func routeGroupKindsEqual(left []v1.RouteGroupKind, right []v1.RouteGroupKind) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]v1.RouteGroupKind(nil), left...)
	right = append([]v1.RouteGroupKind(nil), right...)
	sort.Slice(left, func(i, j int) bool { return string(left[i].Kind) < string(left[j].Kind) })
	sort.Slice(right, func(i, j int) bool { return string(right[i].Kind) < string(right[j].Kind) })
	for i := range left {
		if lo.FromPtr(left[i].Group) != lo.FromPtr(right[i].Group) || left[i].Kind != right[i].Kind {
			return false
		}
	}
	return true
}

func conditionsSemanticallyEqual(left []metav1.Condition, right []metav1.Condition) bool {
	if len(left) != len(right) {
		return false
	}
	leftByType := lo.SliceToMap(left, func(condition metav1.Condition) (string, metav1.Condition) {
		return condition.Type, condition
	})
	for _, rightCondition := range right {
		leftCondition, found := leftByType[rightCondition.Type]
		if !found ||
			leftCondition.Status != rightCondition.Status ||
			leftCondition.Reason != rightCondition.Reason ||
			leftCondition.Message != rightCondition.Message ||
			leftCondition.ObservedGeneration != rightCondition.ObservedGeneration {
			return false
		}
	}
	return true
}
