package app

import (
	"context"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/types"
	"go.uber.org/dig"
	v1 "k8s.io/api/core/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type syncBackendEndpointsParams struct {
	gateway             gatewayv1.Gateway
	httpRoute           gatewayv1.HTTPRoute
	config              types.GatewayConfig
	resolvedBackendRefs map[string]v1.Service
}

// httpBackendModel defines the interface for managing OCI backend sets based on HTTPRoute definitions.
type httpBackendModel interface {
	// syncBackendEndpoints synchronizes the OCI Load Balancer Backend Sets associated with the
	// provided HTTPRoute, ensuring they contain the correct set of ready endpoints
	// derived from the referenced Kubernetes Services' EndpointSlices.
	syncBackendEndpoints(ctx context.Context, params syncBackendEndpointsParams) error
}

// httpBackendModelDeps contains the dependencies for the HTTPBackendModel.
type httpBackendModelDeps struct {
	dig.In

	RootLogger *slog.Logger
	K8sClient  k8sClient // Assuming k8sClient interface is defined/accessible
	// OCIClient  ociClient // Placeholder for OCI client interface
}

// newHTTPBackendModel creates a new HTTPBackendModel.
func newHTTPBackendModel(_ httpBackendModelDeps) httpBackendModel {
	panic("NewHTTPBackendModel not implemented") // TDD Step: Start with panic
}
