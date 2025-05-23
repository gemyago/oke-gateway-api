package k8s

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/app"
	"github.com/go-logr/logr"
	"go.uber.org/dig"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/event"
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

	// feature flags
	ReconcileGatewayClass bool `name:"config.features.reconcileGatewayClass"`
	ReconcileGateway      bool `name:"config.features.reconcileGateway"`
	ReconcileHTTPRoute    bool `name:"config.features.reconcileHTTPRoute"`
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

	if deps.ReconcileGatewayClass {
		if err := builder.ControllerManagedBy(mgr).
			For(&gatewayv1.GatewayClass{}).
			WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
			Complete(wireupReconciler(deps.GatewayClassCtrl, middlewares...)); err != nil {
			return fmt.Errorf("failed to setup GatewayClass controller: %w", err)
		}
	} else {
		logger.InfoContext(loggerCtx, "GatewayClass controller is disabled")
	}

	if deps.ReconcileGateway {
		if err := builder.ControllerManagedBy(mgr).
			For(
				&gatewayv1.Gateway{},

				// Applying predicates just on the gateway level. Secrets do not have generation incremented
				// so secret updates will not trigger a reconciliation.
				builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})),
			).
			Watches(
				&corev1.Secret{},
				handler.EnqueueRequestsFromMapFunc(deps.WatchesModel.MapSecretToGateway),
				builder.WithPredicates(predicate.And(
					predicate.Funcs{
						// We ignore create events. They're also happening when controller starts up
						// which leads to duplicate reconciliations and just noise.
						// We don't care when new secrets are created, currently if no secret
						// the whole gateway reconciliation will fail. We may want to change this
						// in the future and revisit this predicate.
						CreateFunc: func(_ event.CreateEvent) bool { return false },
					},
					predicate.ResourceVersionChangedPredicate{},
				)),
			).
			Complete(wireupReconciler(deps.GatewayCtrl, middlewares...)); err != nil {
			return fmt.Errorf("failed to setup Gateway controller: %w", err)
		}
	} else {
		logger.InfoContext(loggerCtx, "Gateway controller is disabled")
	}

	if deps.ReconcileHTTPRoute {
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
	} else {
		logger.InfoContext(loggerCtx, "HTTPRoute controller is disabled")
	}

	logger.InfoContext(loggerCtx, "Starting controller manager")
	return mgr.Start(loggerCtx)
}
