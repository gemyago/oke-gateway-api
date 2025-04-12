package app

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/dig"
	"k8s.io/apimachinery/pkg/api/errors"
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
	r.logger.InfoContext(ctx, fmt.Sprintf("Reconciling GatewayClass %s", req.NamespacedName))
	var gatewayClass gatewayv1.GatewayClass
	if err := r.client.Get(ctx, req.NamespacedName, &gatewayClass); err != nil {
		if errors.IsNotFound(err) {
			r.logger.InfoContext(ctx, fmt.Sprintf("GatewayClass %s removed", req.NamespacedName))
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get GatewayClass %s: %w", req.NamespacedName, err)
	}

	r.logger.DebugContext(ctx, "Performing reconciliation",
		slog.Any("req", req),
		slog.Any("gatewayClass", gatewayClass),
	)

	// For now, we just log the reconciliation and do nothing
	return reconcile.Result{}, nil
}
