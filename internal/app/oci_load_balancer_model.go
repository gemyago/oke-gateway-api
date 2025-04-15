package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const defaultBackendSetPort = 80

type programDefaultBackendParams struct {
	loadBalancerId   string
	knownBackendSets map[string]loadbalancer.BackendSet
	gateway          *gatewayv1.Gateway
}

type programHttpListenerParams struct {
	loadBalancerId        string
	knownListeners        map[string]loadbalancer.Listener
	defaultBackendSetName string
	listenerSpec          *gatewayv1.Listener
}

type ociLoadBalancerModel interface {
	programDefaultBackendSet(
		ctx context.Context,
		params programDefaultBackendParams,
	) (loadbalancer.BackendSet, error)
	programHttpListener(
		ctx context.Context,
		params programHttpListenerParams,
	) (loadbalancer.Listener, error)
}

type ociLoadBalancerModelImpl struct {
	ociClient ociLoadBalancerClient
	logger    *slog.Logger
}

func (m *ociLoadBalancerModelImpl) programDefaultBackendSet(
	ctx context.Context,
	params programDefaultBackendParams,
) (loadbalancer.BackendSet, error) {
	defaultBackendSetName := params.gateway.Name + "-default"
	if _, ok := params.knownBackendSets[defaultBackendSetName]; ok {
		return params.knownBackendSets[defaultBackendSetName], nil
	}

	m.logger.InfoContext(ctx, "Default backend set not found, creating",
		slog.String("loadBalancerId", params.loadBalancerId),
		slog.String("name", defaultBackendSetName),
	)
	_, err := m.ociClient.CreateBackendSet(ctx, loadbalancer.CreateBackendSetRequest{
		LoadBalancerId: &params.loadBalancerId,
		CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
			Name:   &defaultBackendSetName,
			Policy: lo.ToPtr("ROUND_ROBIN"),
			HealthChecker: &loadbalancer.HealthCheckerDetails{
				Protocol: lo.ToPtr("TCP"),
				Port:     lo.ToPtr(int(defaultBackendSetPort)),
			},
		},
	})
	if err != nil {
		return loadbalancer.BackendSet{},
			fmt.Errorf("failed to create default backend set %s: %w", defaultBackendSetName, err)
	}

	res, err := m.ociClient.GetBackendSet(ctx, loadbalancer.GetBackendSetRequest{
		BackendSetName: &defaultBackendSetName,
		LoadBalancerId: lo.ToPtr(params.loadBalancerId),
	})
	if err != nil {
		return loadbalancer.BackendSet{}, fmt.Errorf("failed to get default backend set %s: %w", defaultBackendSetName, err)
	}

	return res.BackendSet, nil
}

func (m *ociLoadBalancerModelImpl) programHttpListener(
	ctx context.Context,
	params programHttpListenerParams,
) (loadbalancer.Listener, error) {
	return loadbalancer.Listener{}, errors.New("not implemented")
}

type ociLoadBalancerModelDeps struct {
	dig.In

	RootLogger *slog.Logger
	OciClient  ociLoadBalancerClient
}

func newOciLoadBalancerModel(deps ociLoadBalancerModelDeps) ociLoadBalancerModel {
	return &ociLoadBalancerModelImpl{
		logger:    deps.RootLogger.WithGroup("oci-load-balancer-model"),
		ociClient: deps.OciClient,
	}
}
