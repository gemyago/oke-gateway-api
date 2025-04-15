package app

import (
	"context"
	"log/slog"

	"go.uber.org/dig"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type programHttpListenerParams struct {
	loadBalancerId string
	listenerSpec   *gatewayv1.Listener
}

type ociLoadBalancerModel interface {
	programHttpListener(
		ctx context.Context,
		params programHttpListenerParams,
	) error
}

type ociLoadBalancerModelImpl struct {
	ociClient ociLoadBalancerClient
	logger    *slog.Logger
}

func (m *ociLoadBalancerModelImpl) programHttpListener(
	ctx context.Context,
	params programHttpListenerParams,
) error {
	return nil
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
