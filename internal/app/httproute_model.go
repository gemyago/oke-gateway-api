package app

import (
	"context"
	"log/slog"

	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// acceptedHTTPRouteDetails holds the relevant details for a reconciled HTTPRoute.
// This struct will be populated when a reconcile request is accepted.
type acceptedHTTPRouteDetails struct {
	acceptedGatewayDetails
	httpRoute gatewayv1.HTTPRoute
}

// httpRouteModel defines the interface for managing HTTPRoute resources.
type httpRouteModel interface {
	// acceptReconcileRequest accepts a reconcile request for an HTTPRoute.
	// It returns true if the request is relevant for this controller and populates
	// the receiver with necessary details.
	// It returns false if the request is not relevant.
	// It may return an error if there was an error processing the request.
	acceptReconcileRequest(
		ctx context.Context,
		req reconcile.Request,
		receiver *acceptedHTTPRouteDetails,
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
func (m *httpRouteModelImpl) acceptReconcileRequest(
	ctx context.Context,
	req reconcile.Request,
	receiver *acceptedHTTPRouteDetails,
) (bool, error) {
	m.logger.DebugContext(ctx, "Received reconcile request for HTTPRoute", "request", req.NamespacedName)
	// Placeholder implementation
	return false, nil
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
