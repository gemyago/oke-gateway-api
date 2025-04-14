package app

import (
	"context"

	"go.uber.org/dig"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type gatewayModel interface {
	reconcile(ctx context.Context, gw *gatewayv1.Gateway) error
}

type gatewayModelImpl struct {
}

func (m *gatewayModelImpl) reconcile(ctx context.Context, gw *gatewayv1.Gateway) error {
	return nil
}

type gatewayModelDeps struct {
	dig.In
}

func newGatewayModel(deps gatewayModelDeps) gatewayModel {
	return &gatewayModelImpl{}
}
