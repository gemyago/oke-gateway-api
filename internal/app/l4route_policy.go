package app

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	apitypes "k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const gatewayAPIGroup = "gateway.networking.k8s.io"
const serviceKind = "Service"

func l4RouteKindAllowed(listener gatewayv1.Listener, routeKind gatewayv1.Kind) bool {
	return routeKindAllowedForListener(listener, routeKind)
}

func routeKindAllowedForListener(listener gatewayv1.Listener, routeKind gatewayv1.Kind) bool {
	if listener.AllowedRoutes == nil || len(listener.AllowedRoutes.Kinds) == 0 {
		return slices.Contains(supportedRouteKindNamesForProtocol(listener.Protocol), routeKind)
	}

	for _, allowedKind := range listener.AllowedRoutes.Kinds {
		group := gatewayAPIGroup
		if allowedKind.Group != nil {
			group = string(*allowedKind.Group)
		}
		if group == gatewayAPIGroup && allowedKind.Kind == routeKind {
			return true
		}
	}
	return false
}

func allowedRouteListeners(
	ctx context.Context,
	k8sClient k8sClient,
	gatewayDetails resolvedGatewayDetails,
	routeNamespace string,
	matchedListeners []gatewayv1.Listener,
	routeKind gatewayv1.Kind,
) ([]gatewayv1.Listener, error) {
	allowed := make([]gatewayv1.Listener, 0, len(matchedListeners))
	for _, listener := range matchedListeners {
		if listener.AllowedRoutes == nil {
			allowed = append(allowed, listener)
			continue
		}
		if len(listener.AllowedRoutes.Kinds) > 0 && !routeKindAllowedForListener(listener, routeKind) {
			continue
		}
		if listener.AllowedRoutes.Namespaces != nil {
			ownerNamespace := effectiveListenerSourceNamespaceForOCIListener(gatewayDetails, listener)
			namespaceAllowed, err := l4RouteNamespaceAllowed(ctx, k8sClient, ownerNamespace, routeNamespace, listener)
			if err != nil {
				return nil, err
			}
			if !namespaceAllowed {
				continue
			}
		}
		allowed = append(allowed, listener)
	}
	return allowed, nil
}

func l4RouteNamespaceAllowed(
	ctx context.Context,
	k8sClient k8sClient,
	gatewayNamespace string,
	routeNamespace string,
	listener gatewayv1.Listener,
) (bool, error) {
	if listener.AllowedRoutes == nil ||
		listener.AllowedRoutes.Namespaces == nil ||
		listener.AllowedRoutes.Namespaces.From == nil {
		return routeNamespace == gatewayNamespace, nil
	}

	switch *listener.AllowedRoutes.Namespaces.From {
	case gatewayv1.NamespacesFromAll:
		return true, nil
	case gatewayv1.NamespacesFromSame:
		return routeNamespace == gatewayNamespace, nil
	case gatewayv1.NamespacesFromSelector:
		if listener.AllowedRoutes.Namespaces.Selector == nil {
			return false, nil
		}
		selector, err := metav1.LabelSelectorAsSelector(listener.AllowedRoutes.Namespaces.Selector)
		if err != nil {
			return false, fmt.Errorf("invalid allowedRoutes namespace selector: %w", err)
		}
		var namespace corev1.Namespace
		if err = k8sClient.Get(ctx, apitypes.NamespacedName{Name: routeNamespace}, &namespace); err != nil {
			return false, fmt.Errorf("failed to get route namespace %s: %w", routeNamespace, err)
		}
		return selector.Matches(labels.Set(namespace.Labels)), nil
	case gatewayv1.NamespacesFromNone:
		return false, nil
	default:
		return false, nil
	}
}

func l4ListenerAllowsRoute(
	ctx context.Context,
	k8sClient k8sClient,
	gatewayNamespace string,
	routeNamespace string,
	listener gatewayv1.Listener,
	routeKind gatewayv1.Kind,
) (bool, error) {
	if !l4RouteKindAllowed(listener, routeKind) {
		return false, nil
	}
	return l4RouteNamespaceAllowed(ctx, k8sClient, gatewayNamespace, routeNamespace, listener)
}

func parentRefTargetsGateway(parentRef gatewayv1.ParentReference) bool {
	group := gatewayAPIGroup
	if parentRef.Group != nil {
		group = string(*parentRef.Group)
	}
	kind := "Gateway"
	if parentRef.Kind != nil {
		kind = string(*parentRef.Kind)
	}
	return group == gatewayAPIGroup && kind == "Gateway"
}

func parentRefTargetsListenerSet(parentRef gatewayv1.ParentReference) bool {
	group := gatewayAPIGroup
	if parentRef.Group != nil {
		group = string(*parentRef.Group)
	}
	if parentRef.Kind == nil {
		return false
	}
	return group == gatewayAPIGroup && string(*parentRef.Kind) == "ListenerSet"
}

func parentRefTargetName(parentRef gatewayv1.ParentReference, routeNamespace string) apitypes.NamespacedName {
	namespace := routeNamespace
	if parentRef.Namespace != nil {
		namespace = string(*parentRef.Namespace)
	}
	return apitypes.NamespacedName{
		Namespace: namespace,
		Name:      string(parentRef.Name),
	}
}

func l4ValidateServiceBackendRef(backendRef gatewayv1.BackendRef) error {
	group := ""
	if backendRef.BackendObjectReference.Group != nil {
		group = string(*backendRef.BackendObjectReference.Group)
	}
	kind := serviceKind
	if backendRef.BackendObjectReference.Kind != nil {
		kind = string(*backendRef.BackendObjectReference.Kind)
	}
	if group != "" || kind != serviceKind {
		return fmt.Errorf("backendRef %s uses unsupported referent %s/%s",
			backendRef.BackendObjectReference.Name,
			group,
			kind,
		)
	}
	return nil
}

func l4BackendRefWeight(backendRef gatewayv1.BackendRef) int {
	if backendRef.Weight == nil {
		return 1
	}
	return int(*backendRef.Weight)
}
