package app

import (
	"context"
	"fmt"

	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

func referenceGrantAllowsServiceBackend(
	ctx context.Context,
	k8sClient k8sClient,
	routeKind gatewayv1.Kind,
	routeNamespace string,
	backendName apitypes.NamespacedName,
) (bool, error) {
	if backendName.Namespace == routeNamespace {
		return true, nil
	}

	var grants gatewayv1beta1.ReferenceGrantList
	if err := k8sClient.List(ctx, &grants, client.InNamespace(backendName.Namespace)); err != nil {
		return false, fmt.Errorf("failed to list ReferenceGrants in namespace %s: %w", backendName.Namespace, err)
	}

	for _, grant := range grants.Items {
		if !referenceGrantHasMatchingFrom(grant, routeKind, routeNamespace) {
			continue
		}
		if referenceGrantHasMatchingServiceTo(grant, backendName.Name) {
			return true, nil
		}
	}
	return false, nil
}

func referenceGrantAllowsSecretRef(
	ctx context.Context,
	k8sClient k8sClient,
	fromKind gatewayv1.Kind,
	fromNamespace string,
	secretName apitypes.NamespacedName,
) (bool, error) {
	if secretName.Namespace == fromNamespace {
		return true, nil
	}

	var grants gatewayv1beta1.ReferenceGrantList
	if err := k8sClient.List(ctx, &grants, client.InNamespace(secretName.Namespace)); err != nil {
		return false, fmt.Errorf("failed to list ReferenceGrants in namespace %s: %w", secretName.Namespace, err)
	}

	for _, grant := range grants.Items {
		if !referenceGrantHasMatchingFrom(grant, fromKind, fromNamespace) {
			continue
		}
		if referenceGrantHasMatchingSecretTo(grant, secretName.Name) {
			return true, nil
		}
	}
	return false, nil
}

func referenceGrantHasMatchingFrom(
	grant gatewayv1beta1.ReferenceGrant,
	routeKind gatewayv1.Kind,
	routeNamespace string,
) bool {
	for _, from := range grant.Spec.From {
		if string(from.Group) == gatewayAPIGroup &&
			from.Kind == routeKind &&
			string(from.Namespace) == routeNamespace {
			return true
		}
	}
	return false
}

func referenceGrantHasMatchingSecretTo(grant gatewayv1beta1.ReferenceGrant, secretName string) bool {
	return referenceGrantHasMatchingCoreTo(grant, "Secret", secretName)
}

func referenceGrantHasMatchingServiceTo(grant gatewayv1beta1.ReferenceGrant, serviceName string) bool {
	return referenceGrantHasMatchingCoreTo(grant, serviceKind, serviceName)
}

func referenceGrantHasMatchingCoreTo(grant gatewayv1beta1.ReferenceGrant, kind gatewayv1.Kind, name string) bool {
	for _, to := range grant.Spec.To {
		if string(to.Group) != "" || to.Kind != kind {
			continue
		}
		if to.Name == nil || string(*to.Name) == name {
			return true
		}
	}
	return false
}
