package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/samber/lo"
	"go.uber.org/dig"
	types "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type resolvedRouteParentDetails struct {
	gatewayDetails acceptedGatewayDetails
	matchedRef     gatewayv1.ParentReference
	httpRoute      gatewayv1.HTTPRoute
}

// httpRouteModel defines the interface for managing HTTPRoute resources.
type httpRouteModel interface {
	// resolveRequestParent resolves the parent details for a given HTTPRoute.
	// It returns true if the request is relevant for this controller and
	// the parent has been resolved.
	resolveRequestParent(
		ctx context.Context,
		req reconcile.Request,
		receiver *resolvedRouteParentDetails,
	) (bool, error)

	// TODO: Add methods for programming OCI based on HTTPRoute, e.g., programBackendSet, programRoutingRules.
}

// httpRouteModelImpl implements the httpRouteModel interface.
type httpRouteModelImpl struct {
	client       k8sClient
	logger       *slog.Logger
	gatewayModel gatewayModel
	// TODO: Add other dependencies like ociLoadBalancerModel if needed for programming logic.
}

// acceptReconcileRequest is a stub implementation.
// TODO: Implement the actual logic to fetch HTTPRoute, validate parent Gateway, etc.
func (m *httpRouteModelImpl) resolveRequestParent(
	ctx context.Context,
	req reconcile.Request,
	receiver *resolvedRouteParentDetails,
) (bool, error) {
	var httpRoute gatewayv1.HTTPRoute
	if err := m.client.Get(ctx, req.NamespacedName, &httpRoute); err != nil {
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
		parentName := types.NamespacedName{
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

// httpRouteModelDeps defines the dependencies required for the httpRouteModel.
type httpRouteModelDeps struct {
	dig.In

	K8sClient    k8sClient
	RootLogger   *slog.Logger
	GatewayModel gatewayModel
}

// newHTTPRouteModel creates a new instance of httpRouteModel.
func newHTTPRouteModel(deps httpRouteModelDeps) httpRouteModel {
	return &httpRouteModelImpl{
		client:       deps.K8sClient,
		logger:       deps.RootLogger.With("component", "httproute-model"),
		gatewayModel: deps.GatewayModel,
	}
}
