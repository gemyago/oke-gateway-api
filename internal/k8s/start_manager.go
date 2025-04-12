package k8s

import (
	"context"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/app"
	"github.com/go-logr/logr"
	"go.uber.org/dig"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// ManagerDeps contains the dependencies for the controller manager.
type ManagerDeps struct {
	dig.In

	RootLogger *slog.Logger
	K8sClient  client.Client
	Controller *app.GatewayClassController
	Config     *rest.Config
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
		},
	)
	if err != nil {
		return err
	}

	// Register the controller with the manager
	err = builder.ControllerManagedBy(mgr).
		For(&gatewayv1.GatewayClass{}).
		Complete(deps.Controller)
	if err != nil {
		return err
	}

	// Start the manager
	logger.InfoContext(loggerCtx, "Starting controller manager")
	return mgr.Start(loggerCtx)
}
