package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// GRPCRouteController watches GRPCRoute resources.
type GRPCRouteController struct {
	logger           *slog.Logger
	grpcRouteModel   grpcRouteModel
	httpBackendModel httpBackendModel
	driftInterval    time.Duration
}

// GRPCRouteControllerDeps contains the dependencies for the GRPCRouteController.
type GRPCRouteControllerDeps struct {
	dig.In

	RootLogger       *slog.Logger
	GRPCRouteModel   grpcRouteModel
	HTTPBackendModel httpBackendModel
	DriftInterval    time.Duration `name:"config.reconcile.drift-interval"`
}

// NewGRPCRouteController creates a new GRPCRouteController.
func NewGRPCRouteController(deps GRPCRouteControllerDeps) *GRPCRouteController {
	return &GRPCRouteController{
		logger:           deps.RootLogger.WithGroup("grpcroute-controller"),
		grpcRouteModel:   deps.GRPCRouteModel,
		httpBackendModel: deps.HTTPBackendModel,
		driftInterval:    deps.DriftInterval,
	}
}

func (r *GRPCRouteController) reconcileResolvedRoute(
	ctx context.Context,
	resolvedData resolvedGRPCRouteDetails,
) (bool, error) {
	if resolvedData.grpcRoute.DeletionTimestamp != nil {
		err := r.grpcRouteModel.deprovisionRoute(ctx, deprovisionGRPCRouteParams{
			config:           resolvedData.gatewayDetails.config,
			grpcRoute:        resolvedData.grpcRoute,
			matchedListeners: resolvedData.matchedListeners,
		})
		if err != nil {
			return false, fmt.Errorf("failed to deprovision route for gateway %s: %w",
				resolvedData.gatewayDetails.gateway.Name, err)
		}
		return false, nil
	}

	acceptedRoute, err := r.grpcRouteModel.acceptRoute(ctx, resolvedData)
	if err != nil {
		return false, fmt.Errorf("failed to accept route: %w", err)
	}
	if acceptedRoute == nil {
		return false, nil
	}

	programmingRequired := r.grpcRouteModel.isProgrammingRequired(resolvedData)
	if !programmingRequired {
		return true, nil
	}

	knownBackends, err := r.grpcRouteModel.resolveBackendRefs(ctx, resolveGRPCBackendRefsParams{
		grpcRoute: *acceptedRoute,
	})
	if err != nil {
		return false, fmt.Errorf("failed to resolve backend refs: %w", err)
	}

	programResult, err := r.grpcRouteModel.programRoute(ctx, programGRPCRouteParams{
		gateway:          resolvedData.gatewayDetails.gateway,
		config:           resolvedData.gatewayDetails.config,
		grpcRoute:        *acceptedRoute,
		matchedListeners: resolvedData.matchedListeners,
		knownBackends:    knownBackends,
	})
	if err != nil {
		return false, fmt.Errorf("failed to program route: %w", err)
	}

	if err = r.grpcRouteModel.setProgrammed(ctx, setGRPCRouteProgrammedParams{
		gatewayClass:          resolvedData.gatewayDetails.gatewayClass,
		gateway:               resolvedData.gatewayDetails.gateway,
		grpcRoute:             *acceptedRoute,
		matchedRef:            resolvedData.matchedRef,
		programmedPolicyRules: programResult.programmedPolicyRules,
	}); err != nil {
		return false, fmt.Errorf("failed to set programmed status: %w", err)
	}

	return true, nil
}

func (r *GRPCRouteController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.logger.InfoContext(ctx, fmt.Sprintf("Processing reconciliation for GRPCRoute %s", req.NamespacedName))

	resolvedRequests, err := r.grpcRouteModel.resolveRequest(ctx, req)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to resolve request parent: %w", err)
	}
	if len(resolvedRequests) == 0 {
		return reconcile.Result{}, nil
	}

	for _, resolvedData := range resolvedRequests {
		var syncEndpointsRequired bool
		syncEndpointsRequired, err = r.reconcileResolvedRoute(ctx, resolvedData)
		if err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to reconcile gateway %s for route %s: %w",
				resolvedData.gatewayDetails.gateway.Name, resolvedData.grpcRoute.Name, err)
		}

		if syncEndpointsRequired {
			err = r.httpBackendModel.syncGRPCRouteEndpoints(ctx, syncGRPCRouteEndpointsParams{
				grpcRoute: resolvedData.grpcRoute,
				config:    resolvedData.gatewayDetails.config,
			})
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("failed to sync backend endpoints: %w", err)
			}
		}
	}

	r.logger.InfoContext(ctx, fmt.Sprintf("Reconciled GRPCRoute %s", req.NamespacedName))

	return driftRequeue(r.driftInterval), nil
}
