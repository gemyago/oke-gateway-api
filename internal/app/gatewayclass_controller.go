package app

import (
	"context"
	"log/slog"

	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// This is an internal interface used only to describe what we need from the client.
type k8sClient interface {
	Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
	List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
}

// GatewayClassController is a simple controller that watches GatewayClass resources.
type GatewayClassController struct {
	client k8sClient
	logger *slog.Logger
}

// GatewayClassControllerDeps contains the dependencies for the GatewayClassController.
type GatewayClassControllerDeps struct {
	dig.In

	RootLogger *slog.Logger
	K8sClient  k8sClient
}

// NewGatewayClassController creates a new GatewayClassController.
func NewGatewayClassController(deps GatewayClassControllerDeps) *GatewayClassController {
	return &GatewayClassController{
		client: deps.K8sClient,
		logger: deps.RootLogger,
	}
}

// Reconcile implements the reconcile.Reconciler interface.
func (r *GatewayClassController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var gatewayClass gatewayv1.GatewayClass
	if err := r.client.Get(ctx, req.NamespacedName, &gatewayClass); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	r.logger.InfoContext(ctx, "Reconciling GatewayClass",
		"name", gatewayClass.Name,
		"controller", gatewayClass.Spec.ControllerName,
	)

	// For now, we just log the reconciliation and do nothing
	return reconcile.Result{}, nil
}
