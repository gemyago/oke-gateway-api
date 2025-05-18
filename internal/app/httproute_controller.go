package app

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// HTTPRouteController is a simple controller that watches HTTPRoute resources.
type HTTPRouteController struct {
	logger           *slog.Logger
	httpRouteModel   httpRouteModel
	httpBackendModel httpBackendModel
}

// HTTPRouteControllerDeps contains the dependencies for the HTTPRouteController.
type HTTPRouteControllerDeps struct {
	dig.In

	RootLogger       *slog.Logger
	HTTPRouteModel   httpRouteModel
	HTTPBackendModel httpBackendModel
}

// NewHTTPRouteController creates a new HTTPRouteController.
func NewHTTPRouteController(deps HTTPRouteControllerDeps) *HTTPRouteController {
	return &HTTPRouteController{
		logger:           deps.RootLogger.WithGroup("httproute-controller"),
		httpRouteModel:   deps.HTTPRouteModel,
		httpBackendModel: deps.HTTPBackendModel,
	}
}

func (r *HTTPRouteController) performProgramming(
	ctx context.Context,
	resolvedData resolvedRouteDetails,
) error {
	r.logger.DebugContext(ctx, "Performing HTTProute programming",
		slog.String("httpRoute", resolvedData.httpRoute.Name),
		slog.String("gateway", resolvedData.gatewayDetails.gateway.Name),
	)

	var acceptedRoute *gatewayv1.HTTPRoute
	acceptedRoute, err := r.httpRouteModel.acceptRoute(ctx, resolvedData)
	if err != nil {
		return fmt.Errorf("failed to accept route: %w", err)
	}

	knownBackends, err := r.httpRouteModel.resolveBackendRefs(ctx, resolveBackendRefsParams{
		httpRoute: *acceptedRoute,
	})
	if err != nil {
		return fmt.Errorf("failed to resolve backend refs: %w", err)
	}

	programResult, err := r.httpRouteModel.programRoute(ctx, programRouteParams{
		gateway:          resolvedData.gatewayDetails.gateway,
		config:           resolvedData.gatewayDetails.config,
		httpRoute:        *acceptedRoute,
		matchedListeners: resolvedData.matchedListeners,
		knownBackends:    knownBackends,
	})
	if err != nil {
		return fmt.Errorf("failed to program route: %w", err)
	}

	// Mark the route as programmed by setting the ResolvedRefs condition
	if err = r.httpRouteModel.setProgrammed(ctx, setProgrammedParams{
		gatewayClass:          resolvedData.gatewayDetails.gatewayClass,
		gateway:               resolvedData.gatewayDetails.gateway,
		httpRoute:             *acceptedRoute,
		matchedRef:            resolvedData.matchedRef,
		programmedPolicyRules: programResult.programmedPolicyRules,
	}); err != nil {
		return fmt.Errorf("failed to set programmed status: %w", err)
	}

	r.logger.InfoContext(ctx, "Successfully programmed HTTProute",
		slog.String("httpRoute", resolvedData.httpRoute.Name),
		slog.String("gateway", resolvedData.gatewayDetails.gateway.Name),
	)

	return nil
}

// Reconcile implements the reconcile.Reconciler interface.
// For now, it just returns a "not implemented" error.
func (r *HTTPRouteController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.logger.InfoContext(ctx, fmt.Sprintf("Processing reconciliation for HTTProute %s", req.NamespacedName))

	resolvedRequests, err := r.httpRouteModel.resolveRequest(ctx, req)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to resolve request parent: %w", err)
	}
	if len(resolvedRequests) == 0 {
		r.logger.InfoContext(ctx, "Ignoring irrelevant HTTPRoute route",
			slog.String("httpRoute", req.NamespacedName.String()),
		)
		return reconcile.Result{}, nil
	}

	for _, resolvedData := range resolvedRequests {
		if resolvedData.httpRoute.DeletionTimestamp != nil {
			r.logger.InfoContext(ctx, "HTTPRoute is marked for deletion, deprovisioning",
				slog.String("httpRoute", req.NamespacedName.String()),
				slog.String("gateway", resolvedData.gatewayDetails.gateway.Name),
			)
			err = r.httpRouteModel.deprovisionRoute(ctx, deprovisionRouteParams{
				gateway:          resolvedData.gatewayDetails.gateway,
				config:           resolvedData.gatewayDetails.config,
				httpRoute:        resolvedData.httpRoute,
				matchedListeners: resolvedData.matchedListeners,
			})
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to deprovision route for gateway %s: %w",
					resolvedData.gatewayDetails.gateway.Name, err)
			}
		} else {
			var programmingRequired bool
			programmingRequired, err = r.httpRouteModel.isProgrammingRequired(resolvedData)
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to check programming requirement for gateway %s: %w",
					resolvedData.gatewayDetails.gateway.Name, err)
			}

			if programmingRequired {
				err = r.performProgramming(ctx, resolvedData)
				if err != nil {
					return reconcile.Result{}, fmt.Errorf("failed to perform programming for gateway %s: %w",
						resolvedData.gatewayDetails.gateway.Name, err)
				}
			} else {
				r.logger.DebugContext(ctx, "HTTPRoute programming not required for parent",
					slog.String("httpRoute", req.NamespacedName.String()),
					slog.String("gateway", resolvedData.gatewayDetails.gateway.Name),
				)
			}
		}

		err = r.httpBackendModel.syncRouteEndpoints(ctx, syncRouteEndpointsParams{
			httpRoute: resolvedData.httpRoute,
			config:    resolvedData.gatewayDetails.config,
		})
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to sync backend endpoints: %w", err)
		}
	}

	r.logger.InfoContext(ctx, fmt.Sprintf("Reconciled HTTProute %s", req.NamespacedName))

	return reconcile.Result{}, nil
}
