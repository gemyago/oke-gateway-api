package k8s

import (
	"context"
	"errors"
	"testing"

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
}
