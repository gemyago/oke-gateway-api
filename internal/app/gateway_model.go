package app

import (
	"context"
	"fmt"

	"go.uber.org/dig"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type resourceStatusError struct {
	conditionType string
	reason        string
	message       string
	cause         error
}

func (e *resourceStatusError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("resource %s is not in status %s: %s: %s", e.conditionType, e.reason, e.message, e.cause)
	}
	return fmt.Sprintf("resource %s is not in status %s: %s", e.conditionType, e.reason, e.message)
}

type gatewayModel interface {
	programGateway(ctx context.Context, gw *gatewayv1.Gateway) error
}

type gatewayModelImpl struct {
}

func (m *gatewayModelImpl) programGateway(_ context.Context, gw *gatewayv1.Gateway) error {
	if gw.Annotations == nil || gw.Annotations[LoadBalancerIDAnnotation] == "" {
		return &resourceStatusError{
			conditionType: ProgrammedGatewayConditionType,
			reason:        MissingAnnotationReason,
			message:       fmt.Sprintf("Gateway is missing load balancer ID annotation '%s'", LoadBalancerIDAnnotation),
		}
	}
	return nil
}

type gatewayModelDeps struct {
	dig.In
}

func newGatewayModel(_ gatewayModelDeps) gatewayModel {
	return &gatewayModelImpl{}
}
