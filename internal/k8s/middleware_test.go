package k8s

import (
	"context"
	"errors"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/app"
	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestErrorHandlingMiddleware(t *testing.T) {
	t.Run("when next succeeds", func(t *testing.T) {
		logger := diag.RootTestLogger()
		wantResult := reconcile.Result{Requeue: true}
		dummyReq := reconcile.Request{NamespacedName: types.NamespacedName{Name: faker.Word()}}
		next := reconcile.TypedFunc[reconcile.Request](
			func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
				assert.Equal(t, dummyReq, req)
				return wantResult, nil
			})

		middleware := newErrorHandlingMiddleware(logger)
		ctrl := middleware(next)

		actualResult, actualErr := ctrl.Reconcile(t.Context(), dummyReq)

		require.NoError(t, actualErr)
		assert.Equal(t, wantResult, actualResult)
	})

	t.Run("when next errors", func(t *testing.T) {
		logger := diag.RootTestLogger()
		wantResult := reconcile.Result{RequeueAfter: 1}
		wantErr := errors.New("reconcile error")
		dummyReq := reconcile.Request{NamespacedName: types.NamespacedName{Name: faker.Word(), Namespace: faker.Word()}}
		next := reconcile.TypedFunc[reconcile.Request](
			func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
				assert.Equal(t, dummyReq, req)
				return wantResult, wantErr
			})

		middleware := newErrorHandlingMiddleware(logger)
		ctrl := middleware(next)

		actualResult, actualErr := ctrl.Reconcile(t.Context(), dummyReq)

		require.Error(t, actualErr)
		assert.Same(t, wantErr, actualErr)
		assert.Equal(t, wantResult, actualResult)
	})

	t.Run("when next errors with non-retriable app.ReconcileError", func(t *testing.T) {
		logger := diag.RootTestLogger()
		dummyErr := app.NewReconcileError(faker.Sentence(), false)
		dummyReq := reconcile.Request{NamespacedName: types.NamespacedName{Name: faker.Word(), Namespace: faker.Word()}}
		next := reconcile.TypedFunc[reconcile.Request](
			func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
				assert.Equal(t, dummyReq, req)
				return reconcile.Result{}, dummyErr
			})

		middleware := newErrorHandlingMiddleware(logger)
		ctrl := middleware(next)

		actualResult, actualErr := ctrl.Reconcile(t.Context(), dummyReq)

		require.NoError(t, actualErr)
		assert.Equal(t, reconcile.Result{}, actualResult)
	})
}

func TestTracingMiddleware(t *testing.T) {
	t.Run("should inject correlation ID and call next", func(t *testing.T) {
		wantResult := reconcile.Result{Requeue: true}
		wantErr := errors.New("dummy error")
		dummyReq := reconcile.Request{NamespacedName: types.NamespacedName{Name: faker.Word()}}
		next := reconcile.TypedFunc[reconcile.Request](
			func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
				require.NotNil(t, ctx)

				attrs := diag.GetLogAttributesFromContext(ctx)
				require.NotNil(t, attrs)
				assert.NotEmpty(t, attrs.CorrelationID.String())

				assert.Equal(t, dummyReq, req)
				return wantResult, wantErr
			})

		middleware := newTracingMiddleware()
		ctrl := middleware(next)

		actualResult, actualErr := ctrl.Reconcile(t.Context(), dummyReq)

		require.ErrorIs(t, actualErr, wantErr)
		assert.Equal(t, wantResult, actualResult)
	})
}

func TestWireupReconciler(t *testing.T) {
	t.Run("should apply middlewares in order", func(t *testing.T) {
		callOrder := []string{}
		dummyReq := reconcile.Request{NamespacedName: types.NamespacedName{Name: faker.Word()}}
		wantResult := reconcile.Result{Requeue: true}
		wantErr := errors.New("final error")

		// Final reconciler
		finalReconciler := reconcile.TypedFunc[reconcile.Request](
			func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
				callOrder = append(callOrder, "final")
				assert.Equal(t, dummyReq, req)
				return wantResult, wantErr
			},
		)

		// Middleware 1
		mw1 := func(next reconcile.TypedReconciler[reconcile.Request]) reconcile.TypedReconciler[reconcile.Request] {
			return reconcile.TypedFunc[reconcile.Request](
				func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
					callOrder = append(callOrder, "mw1_before")
					res, err := next.Reconcile(ctx, req)
					callOrder = append(callOrder, "mw1_after")
					return res, err
				},
			)
		}

		// Middleware 2
		mw2 := func(next reconcile.TypedReconciler[reconcile.Request]) reconcile.TypedReconciler[reconcile.Request] {
			return reconcile.TypedFunc[reconcile.Request](
				func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
					callOrder = append(callOrder, "mw2_before")
					res, err := next.Reconcile(ctx, req)
					callOrder = append(callOrder, "mw2_after")
					return res, err
				},
			)
		}

		wiredCtrl := wireupReconciler(finalReconciler, mw1, mw2)
		actualResult, actualErr := wiredCtrl.Reconcile(t.Context(), dummyReq)

		assert.Equal(t, wantResult, actualResult)
		require.ErrorIs(t, actualErr, wantErr)
		assert.Equal(t, []string{"mw1_before", "mw2_before", "final", "mw2_after", "mw1_after"}, callOrder)
	})

	t.Run("should work with no middlewares", func(t *testing.T) {
		called := false
		dummyReq := reconcile.Request{NamespacedName: types.NamespacedName{Name: faker.Word()}}
		wantResult := reconcile.Result{Requeue: false}
		wantErr := errors.New("no mw error")

		finalReconciler := reconcile.TypedFunc[reconcile.Request](
			func(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
				called = true
				assert.Equal(t, dummyReq, req)
				return wantResult, wantErr
			},
		)

		wiredCtrl := wireupReconciler(finalReconciler)
		actualResult, actualErr := wiredCtrl.Reconcile(t.Context(), dummyReq)

		assert.True(t, called, "final reconciler should have been called")
		assert.Equal(t, wantResult, actualResult)
		assert.ErrorIs(t, actualErr, wantErr)
	})
}
