package app

import (
	"context"
	"log/slog"

	"go.uber.org/dig"
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
	client k8sClient
	logger *slog.Logger
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

	receiver.httpRoute = httpRoute

	return true, nil
}

// httpRouteModelDeps defines the dependencies required for the httpRouteModel.
type httpRouteModelDeps struct {
	dig.In

	K8sClient  k8sClient
	RootLogger *slog.Logger
	// TODO: Add other dependencies as needed.
}

// newHTTPRouteModel creates a new instance of httpRouteModel.
func newHTTPRouteModel(deps httpRouteModelDeps) httpRouteModel {
	return &httpRouteModelImpl{
		client: deps.K8sClient,
		logger: deps.RootLogger.With("component", "httproute-model"), // Add context to logger
	}
}
