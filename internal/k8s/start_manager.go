package k8s

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/app"
	"github.com/go-logr/logr"
	"go.uber.org/dig"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// ManagerDeps contains the dependencies for the controller manager.
type ManagerDeps struct {
	dig.In

	RootLogger       *slog.Logger
	K8sClient        client.Client
	GatewayClassCtrl *app.GatewayClassController
	GatewayCtrl      *app.GatewayController
	Config           *rest.Config
}

// StartManager starts the controller manager.
func StartManager(ctx context.Context, deps ManagerDeps) error { // coverage-ignore -- challenging to test
	logger := deps.RootLogger.WithGroup("k8s")

	rlogLogger := logr.FromSlogHandler(logger.Handler())
	loggerCtx := logr.NewContext(ctx, rlogLogger)
	log.SetLogger(rlogLogger)

	// Create a new manager
	mgr, err := manager.New(
		deps.Config,
		manager.Options{
			Scheme: deps.K8sClient.Scheme(),

			// TODO: Per reconciliation correlation is required
			// BaseContext: func() context.Context {
			// 	return diag.SetLogAttributesToContext(ctx, diag.LogAttributes{
			// 		CorrelationID: slog.StringValue(uuid.New().String()),
			// 	})
			// },
		},
	)
	if err != nil {
		return err
	}

	middlewares := []controllerMiddleware[reconcile.Request]{
		newTracingMiddleware(),
		newErrorHandlingMiddleware(deps.RootLogger),
	}

	// Register the Gateway controller
	if err = builder.ControllerManagedBy(mgr).
		For(&gatewayv1.Gateway{}).
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		Complete(wireupReconciler(deps.GatewayCtrl, middlewares...)); err != nil {
		return fmt.Errorf("failed to setup Gateway controller: %w", err)
	}

	// Register the GatewayClass controller
	if err = builder.ControllerManagedBy(mgr).
		For(&gatewayv1.GatewayClass{}).
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		Complete(wireupReconciler(deps.GatewayClassCtrl, middlewares...)); err != nil {
		return fmt.Errorf("failed to setup GatewayClass controller: %w", err)
	}

	// Start the manager
	logger.InfoContext(loggerCtx, "Starting controller manager")
	return mgr.Start(loggerCtx)
}
