package k8s

import (
	"context"
	"errors"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/app"
	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/google/uuid"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type controllerMiddleware[request comparable] func(
	next reconcile.TypedReconciler[request],
) reconcile.TypedReconciler[request]

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
		// TODO: Handle and do not requeue some errors
		// like 4xx errors from OCI

		return reconcile.TypedFunc[reconcile.Request](
			func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
				res, err := next.Reconcile(ctx, req)
				if err != nil {
					var reconcileErr *app.ReconcileError
					if errors.As(err, &reconcileErr) && !reconcileErr.IsRetriable() {
						// Non-retriable error, do not requeue
						logger.ErrorContext(ctx, "Non-retriable reconcile error, skipping requeue",
							slog.Any("request", req),
							diag.ErrAttr(err),
						)
						return reconcile.Result{}, nil // Return nil error to stop reconciliation
					}

					logger.ErrorContext(ctx, "Reconcile failed",
						slog.Any("request", req),
						diag.ErrAttr(err),
					)
				}
				return res, err
			})
	}
}

func wireupReconciler(
	ctrl reconcile.TypedReconciler[reconcile.Request],
	middlewares ...controllerMiddleware[reconcile.Request],
) reconcile.TypedReconciler[reconcile.Request] {
	for i := len(middlewares) - 1; i >= 0; i-- {
		ctrl = middlewares[i](ctrl)
	}
	return ctrl
}
