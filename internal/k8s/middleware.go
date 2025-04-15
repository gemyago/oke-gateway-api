package k8s

import (
	"context"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/google/uuid"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type controllerMiddleware[request comparable] func(next reconcile.TypedReconciler[request]) reconcile.TypedReconciler[request]

func newTracingMiddleware() controllerMiddleware[reconcile.Request] {
	return func(next reconcile.TypedReconciler[reconcile.Request]) reconcile.TypedReconciler[reconcile.Request] {
		return reconcile.TypedFunc[reconcile.Request](
			func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
				diagCtx := diag.SetLogAttributesToContext(ctx, diag.LogAttributes{
					CorrelationID: slog.StringValue(uuid.New().String()),
				})
				return next.Reconcile(diagCtx, req)
			},
		)
	}
}

func newErrorHandlingMiddleware(
	logger *slog.Logger,
) controllerMiddleware[reconcile.Request] {
	return func(next reconcile.TypedReconciler[reconcile.Request]) reconcile.TypedReconciler[reconcile.Request] {
		return reconcile.TypedFunc[reconcile.Request](func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
			res, err := next.Reconcile(ctx, req)
			if err != nil {
				logger.ErrorContext(ctx, "Reconcile failed", "error", err)
			}
			return res, err
		})
	}
}

func wireupReconciler(
	ctrl reconcile.TypedReconciler[reconcile.Request],
	middlewares ...controllerMiddleware[reconcile.Request],
) reconcile.TypedReconciler[reconcile.Request] {
	for _, middleware := range middlewares {
		ctrl = middleware(ctrl)
	}
	return ctrl
}
