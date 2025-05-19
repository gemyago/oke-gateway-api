package k8s

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/app"
	"github.com/go-logr/logr"
	"go.uber.org/dig"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// StartManagerDeps contains the dependencies for the controller manager.
type StartManagerDeps struct {
	dig.In

	RootLogger       *slog.Logger
	Manager          manager.Manager
	GatewayClassCtrl *app.GatewayClassController
	GatewayCtrl      *app.GatewayController
	HTTPRouteCtrl    *app.HTTPRouteController
	WatchesModel     *app.WatchesModel
	Config           *rest.Config
}

// StartManager starts the controller manager.
func StartManager(ctx context.Context, deps StartManagerDeps) error { // coverage-ignore -- challenging to test
	logger := deps.RootLogger.WithGroup("k8s")

	rlogLogger := logr.FromSlogHandler(logger.Handler())
	loggerCtx := logr.NewContext(ctx, rlogLogger)
	log.SetLogger(rlogLogger)

	mgr := deps.Manager

	if err := deps.WatchesModel.RegisterFieldIndexers(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("failed to register field indexers: %w", err)
	}

	middlewares := []controllerMiddleware[reconcile.Request]{
		newTracingMiddleware(),
		newErrorHandlingMiddleware(deps.RootLogger),
	}

	if err := builder.ControllerManagedBy(mgr).
		For(&gatewayv1.GatewayClass{}).
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		Complete(wireupReconciler(deps.GatewayClassCtrl, middlewares...)); err != nil {
		return fmt.Errorf("failed to setup GatewayClass controller: %w", err)
	}

	if err := builder.ControllerManagedBy(mgr).
		For(&gatewayv1.Gateway{}).
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		Complete(wireupReconciler(deps.GatewayCtrl, middlewares...)); err != nil {
		return fmt.Errorf("failed to setup Gateway controller: %w", err)
	}

	if err := builder.ControllerManagedBy(mgr).
		For(&gatewayv1.HTTPRoute{}).
		Watches(
			&discoveryv1.EndpointSlice{},
			handler.EnqueueRequestsFromMapFunc(deps.WatchesModel.MapEndpointSliceToHTTPRoute),
		).
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		Complete(wireupReconciler(deps.HTTPRouteCtrl, middlewares...)); err != nil {
		return fmt.Errorf("failed to setup HTTPRoute controller: %w", err)
	}

	logger.InfoContext(loggerCtx, "Starting controller manager")
	return mgr.Start(loggerCtx)
}
