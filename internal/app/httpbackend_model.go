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

type syncRouteBackendEndpointsParams struct {
	httpRoute gatewayv1.HTTPRoute
	config    types.GatewayConfig
}

type syncRouteBackendRuleEndpointsParams struct {
	httpRoute gatewayv1.HTTPRoute
	config    types.GatewayConfig
	ruleIndex int
}

type identifyBackendsToUpdateParams struct {
	endpointPort    int32
	currentBackends []loadbalancer.Backend
	endpointSlices  []discoveryv1.EndpointSlice
}

type identifyBackendsToUpdateResult struct {
	updateRequired  bool
	updatedBackends []loadbalancer.BackendDetails
}

// httpBackendModel defines the interface for managing OCI backend sets based on HTTPRoute definitions.
type httpBackendModel interface {
	// syncRouteBackendEndpoints synchronizes the OCI Load Balancer Backend Sets associated with the
	// provided HTTPRoute, ensuring they contain the correct set of ready endpoints
	// derived from the referenced Kubernetes Services' EndpointSlices.
	syncRouteBackendEndpoints(ctx context.Context, params syncRouteBackendEndpointsParams) error

	// identifyBackendsToUpdate identifies the backends that need to be updated in the OCI Load Balancer Backend Set.
	// It will correctly handle endpoint status changes, including draining endpoints.
	identifyBackendsToUpdate(
		ctx context.Context,
		params identifyBackendsToUpdateParams,
	) (identifyBackendsToUpdateResult, error)

	// syncRouteBackendRuleEndpoints synchronizes the OCI Load Balancer Backend Sets associated with the
	// single rule of the provided HTTPRoute.
	syncRouteBackendRuleEndpoints(ctx context.Context, params syncRouteBackendRuleEndpointsParams) error
}

type httpBackendModelImpl struct {
	logger              *slog.Logger
	k8sClient           k8sClient
	ociClient           ociLoadBalancerClient
	workRequestsWatcher workRequestsWatcher

	// Used to allow mocking own methods in tests
	self httpBackendModel
}

func (m *httpBackendModelImpl) syncRouteBackendEndpoints(
	ctx context.Context,
	params syncRouteBackendEndpointsParams,
) error {
	m.logger.InfoContext(ctx, "Syncing backend endpoints",
		slog.String("httpRoute", params.httpRoute.Name),
		slog.String("config", params.config.Name),
	)

	for index := range params.httpRoute.Spec.Rules {
		if err := m.self.syncRouteBackendRuleEndpoints(ctx, syncRouteBackendRuleEndpointsParams{
			httpRoute: params.httpRoute,
			config:    params.config,
			ruleIndex: index,
		}); err != nil {
			return fmt.Errorf("failed to sync route backend endpoints for rule %d: %w", index, err)
		}
	}

	return nil
}

func (m *httpBackendModelImpl) identifyBackendsToUpdate(
	ctx context.Context,
	params identifyBackendsToUpdateParams,
) (identifyBackendsToUpdateResult, error) {
	desiredBackendsMap := make(map[string]loadbalancer.BackendDetails)

	for _, slice := range params.endpointSlices {
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
				continue
			}
			if len(endpoint.Addresses) == 0 {
				m.logger.WarnContext(ctx, "Endpoint has no addresses", slog.Any("endpoint", endpoint))
				continue
			}
			ipAddress := endpoint.Addresses[0]
			isDraining := endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating

			desiredBackendsMap[ipAddress] = loadbalancer.BackendDetails{
				Port:      lo.ToPtr(int(params.endpointPort)),
				IpAddress: &ipAddress,
				Drain:     lo.ToPtr(isDraining),
				// Weight, MaxConnections, Backup, Offline are not managed here
			}
		}
	}

	currentBackendsMap := lo.SliceToMap(
		lo.Filter(params.currentBackends, func(b loadbalancer.Backend, _ int) bool {
			return b.IpAddress != nil
		}),
		func(b loadbalancer.Backend) (string, loadbalancer.Backend) {
			return *b.IpAddress, b
		},
	)

	updateRequired := false
	if len(desiredBackendsMap) != len(currentBackendsMap) {
		updateRequired = true
	} else {
		for ip, desired := range desiredBackendsMap {
			current, exists := currentBackendsMap[ip]
			if !exists || lo.FromPtr(desired.Drain) != lo.FromPtr(current.Drain) {
				updateRequired = true
				break
			}
		}
	}

	updatedBackends := lo.Values(desiredBackendsMap)

	return identifyBackendsToUpdateResult{
		updateRequired:  updateRequired,
		updatedBackends: updatedBackends,
	}, nil
}

func (m *httpBackendModelImpl) syncRouteBackendRuleEndpoints(
	ctx context.Context,
	params syncRouteBackendRuleEndpointsParams,
) error {
	rule := params.httpRoute.Spec.Rules[params.ruleIndex]

	backendSetName := backendSetName(params.httpRoute, rule, params.ruleIndex)
	var ruleBackends []loadbalancer.BackendDetails
	firstRefPort := int32(*rule.BackendRefs[0].BackendObjectReference.Port)

	drainingCount := 0
	for _, backendRef := range rule.BackendRefs {
		var endpointSlices discoveryv1.EndpointSliceList

		// TODO: Paginate?
		if err := m.k8sClient.List(ctx, &endpointSlices, client.MatchingLabels{
			discoveryv1.LabelServiceName: string(backendRef.BackendObjectReference.Name),
		}); err != nil {
			return fmt.Errorf("failed to list endpoint slices for backend %s: %w", backendRef.BackendObjectReference.Name, err)
		}

		refPort := int32(*backendRef.BackendObjectReference.Port)

		refBackends := make([]loadbalancer.BackendDetails, 0, len(endpointSlices.Items))
		for _, endpointSlice := range endpointSlices.Items {
			for _, endpoint := range endpointSlice.Endpoints {
				// Skip endpoint if it's explicitly marked as not ready
				if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
					continue
				}

				// Determine if the backend should be marked for draining
				isDraining := endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating

				if isDraining {
					m.logger.DebugContext(ctx, "Draining endpoint",
						slog.String("endpoint", endpoint.Addresses[0]),
					)
					drainingCount++
				}

				refBackends = append(refBackends, loadbalancer.BackendDetails{
					Port:      lo.ToPtr(int(refPort)),
					IpAddress: &endpoint.Addresses[0],
					Drain:     lo.ToPtr(isDraining),
				})
			}
		}

		ruleBackends = append(ruleBackends, refBackends...)
	}

	m.logger.InfoContext(ctx, "Syncing backend endpoints for rule",
		slog.Int("ruleIndex", params.ruleIndex),
		slog.String("httpRoute", params.httpRoute.Name),
		slog.String("backendSetName", backendSetName),
		slog.Int("ruleBackends", len(ruleBackends)),
		slog.Int("drainingBackends", drainingCount),
	)

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
	return nil
}

// httpBackendModelDeps contains the dependencies for the HTTPBackendModel.
type httpBackendModelDeps struct {
	dig.In `ignore-unexported:"true"`

	RootLogger            *slog.Logger
	K8sClient             k8sClient
	OciLoadBalancerClient ociLoadBalancerClient
	WorkRequestsWatcher   workRequestsWatcher

	// Used to allow mocking own methods in tests
	self httpBackendModel
}

// newHTTPBackendModel creates a new HTTPBackendModel.
func newHTTPBackendModel(deps httpBackendModelDeps) httpBackendModel {
	model := &httpBackendModelImpl{
		logger:              deps.RootLogger.WithGroup("http-backend-model"),
		k8sClient:           deps.K8sClient,
		ociClient:           deps.OciLoadBalancerClient,
		workRequestsWatcher: deps.WorkRequestsWatcher,
		self:                deps.self,
	}
	model.self = lo.Ternary[httpBackendModel](model.self != nil, model.self, model)
	return model
}
