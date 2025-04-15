package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"go.uber.org/dig"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type programDefaultBackendParams struct {
	loadBalancerId string
	gateway        *gatewayv1.Gateway
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
	return loadbalancer.BackendSet{}, errors.New("not implemented")
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
