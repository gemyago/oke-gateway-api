package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/types"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	discoveryv1 "k8s.io/api/discovery/v1"
	client "sigs.k8s.io/controller-runtime/pkg/client"
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

	for index, rule := range params.httpRoute.Spec.Rules {
		backendSetName := backendSetName(params.httpRoute, rule, index)
		var ruleBackends []loadbalancer.BackendDetails
		firstRefPort := int32(*rule.BackendRefs[0].BackendObjectReference.Port)
		for _, backendRef := range rule.BackendRefs {
			var endpointSlices discoveryv1.EndpointSliceList
			if err := m.k8sClient.List(ctx, &endpointSlices, client.MatchingLabels{
				discoveryv1.LabelServiceName: string(backendRef.BackendObjectReference.Name),
			}); err != nil {
				return fmt.Errorf("failed to list endpoint slices for backend %s: %w", backendRef.BackendObjectReference.Name, err)
			}

			refPort := int32(*backendRef.BackendObjectReference.Port)

			refBackends := make([]loadbalancer.BackendDetails, 0, len(endpointSlices.Items))
			for _, endpointSlice := range endpointSlices.Items {
				for _, endpoint := range endpointSlice.Endpoints {
					refBackends = append(refBackends, loadbalancer.BackendDetails{
						Port:      lo.ToPtr(int(refPort)),
						IpAddress: &endpoint.Addresses[0],
					})
				}
			}

			ruleBackends = append(ruleBackends, refBackends...)
		}

		ociBackendSet, err := m.ociClient.UpdateBackendSet(ctx, loadbalancer.UpdateBackendSetRequest{
			LoadBalancerId: &params.config.Spec.LoadBalancerID,
			BackendSetName: &backendSetName,
			UpdateBackendSetDetails: loadbalancer.UpdateBackendSetDetails{
				Backends: ruleBackends,

				// TODO: Better fetch the HC from existing backend set
				// route reconciliation is managing it
				Policy: lo.ToPtr("ROUND_ROBIN"),
				HealthChecker: &loadbalancer.HealthCheckerDetails{
					Protocol: lo.ToPtr("TCP"),
					Port:     lo.ToPtr(int(firstRefPort)),
				},
			},
		})
		if err != nil {
			return fmt.Errorf("failed to update backend set %s: %w", backendSetName, err)
		}

		err = m.workRequestsWatcher.WaitFor(ctx, *ociBackendSet.OpcWorkRequestId)
		if err != nil {
			return fmt.Errorf("failed to wait for backend set %s to be updated: %w", backendSetName, err)
		}
	}

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
