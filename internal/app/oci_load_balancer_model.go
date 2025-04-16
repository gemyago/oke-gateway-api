package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const defaultBackendSetPort = 80

type programDefaultBackendParams struct {
	loadBalancerID   string
	knownBackendSets map[string]loadbalancer.BackendSet
	gateway          *gatewayv1.Gateway
}

type programHTTPListenerParams struct {
	loadBalancerID        string
	knownListeners        map[string]loadbalancer.Listener
	defaultBackendSetName string
	listenerSpec          *gatewayv1.Listener
}

type ociLoadBalancerModel interface {
	programDefaultBackendSet(
		ctx context.Context,
		params programDefaultBackendParams,
	) (loadbalancer.BackendSet, error)
	programHTTPListener(
		ctx context.Context,
		params programHTTPListenerParams,
	) (loadbalancer.Listener, error)
}

type ociLoadBalancerModelImpl struct {
	ociClient           ociLoadBalancerClient
	logger              *slog.Logger
	workRequestsWatcher workRequestsWatcher
}

func (m *ociLoadBalancerModelImpl) programDefaultBackendSet(
	ctx context.Context,
	params programDefaultBackendParams,
) (loadbalancer.BackendSet, error) {
	defaultBackendSetName := params.gateway.Name + "-default"
	if _, ok := params.knownBackendSets[defaultBackendSetName]; ok {
		m.logger.DebugContext(ctx, "Default backend set already exists",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("backendName", defaultBackendSetName),
		)
		return params.knownBackendSets[defaultBackendSetName], nil
	}

	m.logger.InfoContext(ctx, "Default backend set not found, creating",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("name", defaultBackendSetName),
	)
	createRes, err := m.ociClient.CreateBackendSet(ctx, loadbalancer.CreateBackendSetRequest{
		LoadBalancerId: &params.loadBalancerID,
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

	if err = m.workRequestsWatcher.WaitFor(
		ctx,
		*createRes.OpcWorkRequestId,
	); err != nil {
		return loadbalancer.BackendSet{},
			fmt.Errorf("failed to wait for default backend set %s: %w", defaultBackendSetName, err)
	}

	res, err := m.ociClient.GetBackendSet(ctx, loadbalancer.GetBackendSetRequest{
		BackendSetName: &defaultBackendSetName,
		LoadBalancerId: lo.ToPtr(params.loadBalancerID),
	})
	if err != nil {
		return loadbalancer.BackendSet{}, fmt.Errorf("failed to get default backend set %s: %w", defaultBackendSetName, err)
	}

	return res.BackendSet, nil
}

func (m *ociLoadBalancerModelImpl) programHTTPListener(
	ctx context.Context,
	params programHTTPListenerParams,
) (loadbalancer.Listener, error) {
	listenerName := string(params.listenerSpec.Name)
	if _, ok := params.knownListeners[listenerName]; ok {
		m.logger.DebugContext(ctx, "Listener already exists",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("listenerName", listenerName),
		)
		return params.knownListeners[listenerName], nil
	}

	m.logger.InfoContext(ctx, "Listener not found, creating",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("name", listenerName),
	)

	createRes, err := m.ociClient.CreateListener(ctx, loadbalancer.CreateListenerRequest{
		LoadBalancerId: &params.loadBalancerID,
		CreateListenerDetails: loadbalancer.CreateListenerDetails{
			Name:                  lo.ToPtr(listenerName),
			DefaultBackendSetName: lo.ToPtr(params.defaultBackendSetName),
			Port:                  lo.ToPtr(int(params.listenerSpec.Port)),
			Protocol:              lo.ToPtr(string(params.listenerSpec.Protocol)),
		},
	})
	if err != nil {
		return loadbalancer.Listener{}, fmt.Errorf("failed to create listener %s: %w", listenerName, err)
	}

	if err = m.workRequestsWatcher.WaitFor(
		ctx,
		*createRes.OpcWorkRequestId,
	); err != nil {
		return loadbalancer.Listener{}, fmt.Errorf("failed to wait for listener %s: %w", listenerName, err)
	}

	res, err := m.ociClient.GetLoadBalancer(ctx, loadbalancer.GetLoadBalancerRequest{
		LoadBalancerId: lo.ToPtr(params.loadBalancerID),
	})
	if err != nil {
		return loadbalancer.Listener{}, fmt.Errorf("failed to get listener %s: %w", listenerName, err)
	}

	// TODO: fail if listener is still not there

	return res.LoadBalancer.Listeners[listenerName], nil
}

type ociLoadBalancerModelDeps struct {
	dig.In

	RootLogger          *slog.Logger
	OciClient           ociLoadBalancerClient
	WorkRequestsWatcher workRequestsWatcher
}

func newOciLoadBalancerModel(deps ociLoadBalancerModelDeps) ociLoadBalancerModel {
	return &ociLoadBalancerModelImpl{
		logger:              deps.RootLogger.WithGroup("oci-load-balancer-model"),
		ociClient:           deps.OciClient,
		workRequestsWatcher: deps.WorkRequestsWatcher,
	}
}
