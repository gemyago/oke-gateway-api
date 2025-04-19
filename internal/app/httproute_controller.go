package app

import (
	"context"
	"log/slog"

	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// HTTPRouteController is a simple controller that watches HTTPRoute resources.
type HTTPRouteController struct {
	client         k8sClient
	logger         *slog.Logger
	resourcesModel resourcesModel
}

// HTTPRouteControllerDeps contains the dependencies for the HTTPRouteController.
type HTTPRouteControllerDeps struct {
	dig.In

	RootLogger     *slog.Logger
	K8sClient      k8sClient
	ResourcesModel resourcesModel
}

// NewHTTPRouteController creates a new HTTPRouteController.
func NewHTTPRouteController(deps HTTPRouteControllerDeps) *HTTPRouteController {
	return &HTTPRouteController{
		client:         deps.K8sClient,
		logger:         deps.RootLogger,
		resourcesModel: deps.ResourcesModel,
	}
}

// Reconcile implements the reconcile.Reconciler interface.
// For now, it just returns a "not implemented" error.
func (r *HTTPRouteController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	// Fetch the HTTPRoute instance
	// var httpRoute gatewayv1.HTTPRoute // Removed as it's unused for now
	// The Get call is included to satisfy basic reconcile structure, but the logic isn't implemented yet.
	// if err := r.client.Get(ctx, req.NamespacedName, &httpRoute); err != nil {
	// 	if errors.IsNotFound(err) {
	// 		r.logger.DebugContext(ctx, fmt.Sprintf("HTTPRoute not present: %s", req.NamespacedName))
	// 		return reconcile.Result{}, nil
	// 	}
	// 	return reconcile.Result{}, fmt.Errorf("failed to get HTTPRoute %s: %w", req.NamespacedName, err)
	// }

	r.logger.DebugContext(ctx, "Reconcile called for HTTPRoute", slog.Any("request", req))

	// TODO: Implement reconciliation logic for HTTPRoute
	return reconcile.Result{}, NewReconcileError("not implemented", false)
}
