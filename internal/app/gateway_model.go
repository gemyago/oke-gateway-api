package app

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/dig"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type resourceStatusError struct {
	conditionType string
	reason        string
	message       string
	cause         error
}

func (e resourceStatusError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf(
			"resourceStatusError: type=%s, reason=%s, message=%s, cause=%s",
			e.conditionType, e.reason, e.message, e.cause)
	}
	return fmt.Sprintf("resourceStatusError: type=%s, reason=%s, message=%s", e.conditionType, e.reason, e.message)
}

type gatewayData struct {
	gateway gatewayv1.Gateway
	config  GatewayConfig
}

type gatewayModel interface {
	// acceptReconcileRequest accepts a reconcile request and returns true if the request is accepted.
	// If returns false if the request is not relevant for this controller.
	// It returns true if the request is relevant for this controller.
	// It may return an error if there was error accepting the request.
	// If error happens, it may not be always known if the request is relevant.
	acceptReconcileRequest(
		ctx context.Context,
		req reconcile.Request,
		receiver *gatewayData,
	) (bool, error)

	programGateway(ctx context.Context, data *gatewayData) error
}

type gatewayModelImpl struct {
	client k8sClient
	logger *slog.Logger
}

func (m *gatewayModelImpl) acceptReconcileRequest(
	ctx context.Context,
	req reconcile.Request,
	receiver *gatewayData,
) (bool, error) {
	receiver.gateway.DeepCopyInto(&receiver.gateway)
	if err := m.client.Get(ctx, req.NamespacedName, &receiver.gateway); err != nil {
		if apierrors.IsNotFound(err) {
			m.logger.InfoContext(ctx, fmt.Sprintf("Gateway %s removed", req.NamespacedName))
			// TODO: We may need to handle deprovisioning, maybe via finalizer?
			return false, nil
		}
		return false, fmt.Errorf("failed to get Gateway %s: %w", req.NamespacedName, err)
	}

	if receiver.gateway.Spec.Infrastructure == nil || receiver.gateway.Spec.Infrastructure.ParametersRef == nil {
		return false, &resourceStatusError{
			conditionType: AcceptedConditionType,
			reason:        MissingConfigReason,
			message:       "spec.infrastructure is missing parametersRef",
		}
	}

	// configName := types.NamespacedName{
	// 	Namespace: receiver.gateway.Namespace,
	// 	Name:      receiver.gateway.Spec.Infrastructure.ParametersRef.Name,
	// }

	// if err := m.client.Get(ctx, configName, &receiver.config); err != nil {
	// 	return false, fmt.Errorf("failed to get GatewayConfig %s: %w", configName, err)
	// }

	return true, nil
}

func (m *gatewayModelImpl) programGateway(_ context.Context, data *gatewayData) error {
	return nil
}

type gatewayModelDeps struct {
	dig.In

	K8sClient  k8sClient
	RootLogger *slog.Logger
}

func newGatewayModel(deps gatewayModelDeps) gatewayModel {
	return &gatewayModelImpl{
		client: deps.K8sClient,
		logger: deps.RootLogger,
	}
}
