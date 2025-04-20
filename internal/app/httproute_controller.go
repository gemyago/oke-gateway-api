package app

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// HTTPRouteController is a simple controller that watches HTTPRoute resources.
type HTTPRouteController struct {
	logger         *slog.Logger
	httpRouteModel httpRouteModel
}

// HTTPRouteControllerDeps contains the dependencies for the HTTPRouteController.
type HTTPRouteControllerDeps struct {
	dig.In

	RootLogger     *slog.Logger
	HTTPRouteModel httpRouteModel
}

// NewHTTPRouteController creates a new HTTPRouteController.
func NewHTTPRouteController(deps HTTPRouteControllerDeps) *HTTPRouteController {
	return &HTTPRouteController{
		logger:         deps.RootLogger,
		httpRouteModel: deps.HTTPRouteModel,
	}
}

// Reconcile implements the reconcile.Reconciler interface.
// For now, it just returns a "not implemented" error.
func (r *HTTPRouteController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.logger.InfoContext(ctx, fmt.Sprintf("Reconciling HTTProute %s", req.NamespacedName))

	var resolvedData resolvedRouteParentDetails
	accepted, err := r.httpRouteModel.resolveRequestParent(ctx, req, &resolvedData)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to resolve request parent: %w", err)
	}
	if !accepted {
		r.logger.InfoContext(ctx, "Ignoring HTTPRoute from irrelevant controller",
			slog.String("httpRoute", req.NamespacedName.String()),
		)
		return reconcile.Result{}, nil
	}

	r.logger.DebugContext(ctx, "Resolved HTTPRoute parent",
		slog.String("httpRoute", req.NamespacedName.String()),
		slog.Any("resolvedDataRef", resolvedData.matchedRef),
	)

	return reconcile.Result{}, nil
}
