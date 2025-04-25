package app

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/dig"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
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
		logger:         deps.RootLogger.WithGroup("httproute-controller"),
		httpRouteModel: deps.HTTPRouteModel,
	}
}

// Reconcile implements the reconcile.Reconciler interface.
// For now, it just returns a "not implemented" error.
func (r *HTTPRouteController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.logger.InfoContext(ctx, fmt.Sprintf("Processing reconciliation for HTTProute %s", req.NamespacedName))

	var resolvedData resolvedRouteDetails
	resolved, err := r.httpRouteModel.resolveRequest(ctx, req, &resolvedData)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to resolve request parent: %w", err)
	}
	if !resolved {
		r.logger.InfoContext(ctx, "Ignoring irrelevant HTTPRoute route",
			slog.String("httpRoute", req.NamespacedName.String()),
		)
		return reconcile.Result{}, nil
	}

	// Check if programming is required based on status
	programmingRequired, err := r.httpRouteModel.isProgrammingRequired(resolvedData)
	if err != nil {
		r.logger.ErrorContext(ctx, "Failed to check if programming is required",
			slog.String("httpRoute", req.NamespacedName.String()),
		)
		return reconcile.Result{}, err
	}
	if programmingRequired {
		var acceptedRoute *gatewayv1.HTTPRoute
		acceptedRoute, err = r.httpRouteModel.acceptRoute(ctx, resolvedData)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to accept route: %w", err)
		}

		var backendRefs map[string]v1.Service
		backendRefs, err = r.httpRouteModel.resolveBackendRefs(ctx, resolveBackendRefsParams{
			httpRoute: *acceptedRoute,
		})
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to resolve backend refs: %w", err)
		}

		err = r.httpRouteModel.programRoute(ctx, programRouteParams{
			gateway:             resolvedData.gatewayDetails.gateway,
			config:              resolvedData.gatewayDetails.config,
			httpRoute:           *acceptedRoute,
			resolvedBackendRefs: backendRefs,
		})
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to program route: %w", err)
		}
	} else {
		r.logger.DebugContext(ctx, "HTTPRoute programming not required",
			slog.String("httpRoute", req.NamespacedName.String()),
		)
	}
	r.logger.InfoContext(ctx, fmt.Sprintf("Reconciled HTTProute %s", req.NamespacedName))

	return reconcile.Result{}, nil
}
