package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"go.uber.org/dig"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	r.logger.InfoContext(ctx, fmt.Sprintf("Reconciling Gateway %s", req.NamespacedName))
	var gateway gatewayv1.Gateway
	if err := r.client.Get(ctx, req.NamespacedName, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			r.logger.InfoContext(ctx, fmt.Sprintf("Gateway %s removed", req.NamespacedName))
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get Gateway %s: %w", req.NamespacedName, err)
	}

	r.logger.DebugContext(ctx, "Gateway reconciliation details",
		slog.Any("req", req),
		slog.Any("gateway", gateway),
	)

	if !r.resourcesModel.isConditionSet(&gateway, gateway.Status.Conditions, ProgrammedGatewayConditionType) {
		if err := r.gatewayModel.programGateway(ctx, &gateway); err != nil {
			return r.processResourceError(ctx, err, &gateway)
		}

		if err := r.resourcesModel.setCondition(ctx, setConditionParams{
			resource:      &gateway,
			conditions:    &gateway.Status.Conditions,
			conditionType: ProgrammedGatewayConditionType,
			status:        v1.ConditionTrue,
			reason:        LoadBalancerReconciledReason,
			message:       fmt.Sprintf("Gateway %s programmed by %s", gateway.Name, ControllerClassName),
		}); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to set accepted condition for Gateway %s: %w", req.NamespacedName, err)
		}

		r.logger.InfoContext(ctx,
			"Successfully set Programmed condition for Gateway",
			slog.String("gateway", req.NamespacedName.String()),
		)
	} else {
		r.logger.DebugContext(ctx,
			"Programmed condition already set for this Gateway generation",
			slog.String("gateway", req.NamespacedName.String()),
			slog.String("generation", strconv.FormatInt(gateway.Generation, 10)),
		)
	}

	return reconcile.Result{}, nil
}
