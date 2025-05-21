package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"go.uber.org/dig"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GatewayController is a simple controller that watches Gateway resources.
type GatewayController struct {
	client         k8sClient
	logger         *slog.Logger
	resourcesModel resourcesModel
	gatewayModel   gatewayModel
}

// GatewayControllerDeps contains the dependencies for the GatewayController.
type GatewayControllerDeps struct {
	dig.In

	RootLogger     *slog.Logger
	K8sClient      k8sClient
	ResourcesModel resourcesModel
	GatewayModel   gatewayModel
}

// NewGatewayController creates a new GatewayController.
func NewGatewayController(deps GatewayControllerDeps) *GatewayController {
	return &GatewayController{
		client:         deps.K8sClient,
		logger:         deps.RootLogger.WithGroup("gateway-controller"),
		resourcesModel: deps.ResourcesModel, // Initialize resourcesModel
		gatewayModel:   deps.GatewayModel,
	}
}

// processResourceError handles errors from resource programming operations.
// It checks if the error is a resourceStatusError and updates the condition accordingly.
// Returns a reconcile result and error to be returned from the Reconcile method.
func (r *GatewayController) processResourceError(
	ctx context.Context,
	err error,
	gateway *gatewayv1.Gateway,
) (reconcile.Result, error) {
	var reasonErr *resourceStatusError
	if errors.As(err, &reasonErr) {
		if err = r.resourcesModel.setCondition(ctx, setConditionParams{
			resource:      gateway,
			conditions:    &gateway.Status.Conditions,
			conditionType: reasonErr.conditionType,
			status:        v1.ConditionFalse,
			reason:        reasonErr.reason,
			message:       reasonErr.message,
		}); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to set condition for Gateway %s: %w", gateway.Name, err)
		}
		r.logger.WarnContext(ctx, "Failed to program gateway",
			slog.String("gateway", gateway.GetName()),
			slog.String("reason", reasonErr.Error()),
		)
		return reconcile.Result{}, nil
	}
	return reconcile.Result{}, fmt.Errorf("failed to program Gateway %s: %w", gateway.Name, err)
}

// Reconcile implements the reconcile.Reconciler interface for Gateway resources.
func (r *GatewayController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.logger.InfoContext(ctx, fmt.Sprintf("Processing reconciliation for Gateway %s", req.NamespacedName))
	var data resolvedGatewayDetails
	relevant, err := r.gatewayModel.resolveReconcileRequest(ctx, req, &data)
	if err != nil {
		return r.processResourceError(ctx, err, &data.gateway)
	}
	if !relevant {
		return reconcile.Result{}, nil
	}

	if !r.resourcesModel.isConditionSet(isConditionSetParams{
		resource:      &data.gateway,
		conditions:    data.gateway.Status.Conditions,
		conditionType: string(gatewayv1.GatewayConditionAccepted),
	}) {
		if err = r.resourcesModel.setCondition(ctx, setConditionParams{
			resource:      &data.gateway,
			conditions:    &data.gateway.Status.Conditions,
			conditionType: string(gatewayv1.GatewayConditionAccepted),
			status:        v1.ConditionTrue,
			reason:        string(gatewayv1.GatewayReasonAccepted),
			message:       fmt.Sprintf("Gateway %s accepted by %s", data.gateway.Name, ControllerClassName),
			annotations: map[string]string{
				ControllerClassName: "true",
			},
		}); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to set accepted condition for Gateway %s: %w", req.NamespacedName, err)
		}
	}

	if !r.gatewayModel.isProgrammed(ctx, &data) {
		r.logger.DebugContext(ctx, "Programming gateway",
			slog.Any("req", req),
			slog.Any("gateway", data.gateway),
			slog.Any("gatewayClass", data.gatewayClass),
			slog.Any("config", data.config),
		)

		if err = r.gatewayModel.programGateway(ctx, &data); err != nil {
			return r.processResourceError(ctx, err, &data.gateway)
		}

		if err = r.gatewayModel.setProgrammed(ctx, &data); err != nil {
			return reconcile.Result{}, err
		}

		r.logger.InfoContext(ctx,
			"Successfully set Programmed condition for Gateway",
			slog.String("gateway", req.NamespacedName.String()),
		)
	} else {
		r.logger.DebugContext(ctx,
			"Programmed condition already set for this Gateway generation",
			slog.String("gateway", req.NamespacedName.String()),
			slog.String("generation", strconv.FormatInt(data.gateway.Generation, 10)),
		)
	}

	return reconcile.Result{}, nil
}
