package app

import (
	"context"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/types"
	"go.uber.org/dig"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type syncBackendEndpointsParams struct {
	httpRoute gatewayv1.HTTPRoute
	config    types.GatewayConfig
}

// httpBackendModel defines the interface for managing OCI backend sets based on HTTPRoute definitions.
type httpBackendModel interface {
	// syncBackendEndpoints synchronizes the OCI Load Balancer Backend Sets associated with the
	// provided HTTPRoute, ensuring they contain the correct set of ready endpoints
	// derived from the referenced Kubernetes Services' EndpointSlices.
	syncBackendEndpoints(ctx context.Context, params syncBackendEndpointsParams) error
}

type httpBackendModelImpl struct {
	logger              *slog.Logger
	k8sClient           k8sClient
	ociClient           ociLoadBalancerClient
	workRequestsWatcher workRequestsWatcher
}

func (m *httpBackendModelImpl) syncBackendEndpoints(ctx context.Context, params syncBackendEndpointsParams) error {
	m.logger.InfoContext(ctx, "Syncing backend endpoints",
		slog.String("httpRoute", params.httpRoute.Name),
		slog.String("config", params.config.Name),
	)

	return nil
}

// httpBackendModelDeps contains the dependencies for the HTTPBackendModel.
type httpBackendModelDeps struct {
	dig.In

	RootLogger            *slog.Logger
	K8sClient             k8sClient
	OciLoadBalancerClient ociLoadBalancerClient
	WorkRequestsWatcher   workRequestsWatcher
}

// newHTTPBackendModel creates a new HTTPBackendModel.
func newHTTPBackendModel(deps httpBackendModelDeps) httpBackendModel {
	return &httpBackendModelImpl{
		logger:              deps.RootLogger.WithGroup("http-backend-model"),
		k8sClient:           deps.K8sClient,
		ociClient:           deps.OciLoadBalancerClient,
		workRequestsWatcher: deps.WorkRequestsWatcher,
	}
}
