package app

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/dig"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GatewayController is a simple controller that watches Gateway resources.
type GatewayController struct {
	client         k8sClient // Reusing the k8sClient interface defined in gatewayclass_controller.go
	logger         *slog.Logger
	resourcesModel resourcesModel // Add resourcesModel field
	gatewayModel   gatewayModel
}

// GatewayControllerDeps contains the dependencies for the GatewayController.
type GatewayControllerDeps struct {
	dig.In

	RootLogger     *slog.Logger
	K8sClient      k8sClient
	ResourcesModel resourcesModel // Add ResourcesModel dependency
	GatewayModel   gatewayModel
}

// NewGatewayController creates a new GatewayController.
func NewGatewayController(deps GatewayControllerDeps) *GatewayController {
	return &GatewayController{
		client:         deps.K8sClient,
		logger:         deps.RootLogger,
		resourcesModel: deps.ResourcesModel, // Initialize resourcesModel
		gatewayModel:   deps.GatewayModel,
	}
}

// Reconcile implements the reconcile.Reconciler interface for Gateway resources.
func (r *GatewayController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.logger.InfoContext(ctx, fmt.Sprintf("Reconciling Gateway %s", req.NamespacedName))
	var gateway gatewayv1.Gateway
	if err := r.client.Get(ctx, req.NamespacedName, &gateway); err != nil {
		if errors.IsNotFound(err) {
			r.logger.InfoContext(ctx, fmt.Sprintf("Gateway %s removed", req.NamespacedName))
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get Gateway %s: %w", req.NamespacedName, err)
	}

	r.logger.DebugContext(ctx, "Gateway reconciliation details",
		slog.Any("req", req),
		slog.Any("gateway", gateway),
	)

	if r.resourcesModel.isConditionSet(&gateway, gateway.Status.Conditions, AcceptedConditionType) {
		r.logger.DebugContext(ctx, "Gateway already accepted", slog.String("gateway", req.NamespacedName.String()))
		return reconcile.Result{}, nil
	}

	if err := r.resourcesModel.setAcceptedCondition(ctx, setAcceptedConditionParams{
		resource:   &gateway,
		conditions: &gateway.Status.Conditions,
		message:    fmt.Sprintf("Gateway %s accepted by controller class %s", gateway.Name, ControllerClassName),
		annotations: map[string]string{
			"oke-gateway-api.oraclecloud.com/managed-by": ControllerClassName,
		},
	}); err != nil {
		return reconcile.Result{},
			fmt.Errorf("failed to set accepted condition for Gateway %s: %w", req.NamespacedName, err)
	}

	r.logger.InfoContext(ctx,
		"Successfully set Accepted condition for Gateway",
		slog.String("gateway", req.NamespacedName.String()),
	)

	return reconcile.Result{}, nil
}
