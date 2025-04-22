package app

import (
	"context"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client" // Import client for ObjectKey
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestHTTPRouteController(t *testing.T) {
	newMockDeps := func(t *testing.T) HTTPRouteControllerDeps {
		return HTTPRouteControllerDeps{
			HTTPRouteModel: NewMockhttpRouteModel(t),
			RootLogger:     diag.RootTestLogger(),
		}
	}

	t.Run("RelevantRoute", func(t *testing.T) {
		deps := newMockDeps(t)
		controller := NewHTTPRouteController(deps)

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: faker.DomainName(), // Use faker for random namespace
				Name:      faker.Word(),       // Use faker for random name
			},
		}

		wantResolvedData := resolvedRouteDetails{
			httpRoute: makeRandomHTTPRoute(),
			gatewayDetails: acceptedGatewayDetails{
				gateway: *newRandomGateway(),
			},
		}

		mockModel, _ := deps.HTTPRouteModel.(*MockhttpRouteModel)
		mockModel.EXPECT().resolveRequest(
			t.Context(),
			req,
			mock.Anything,
		).RunAndReturn(func(
			_ context.Context,
			_ reconcile.Request,
			receiver *resolvedRouteDetails,
		) (bool, error) {
			*receiver = wantResolvedData
			return true, nil
		})

		mockModel.EXPECT().acceptRoute(
			t.Context(),
			&wantResolvedData,
		).Return(nil, nil)

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})

	t.Run("IrrelevantRoute", func(t *testing.T) {
		deps := newMockDeps(t)
		controller := NewHTTPRouteController(deps)

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: faker.DomainName(),
				Name:      faker.Word(),
			},
		}

		mockModel, _ := deps.HTTPRouteModel.(*MockhttpRouteModel)
		mockModel.EXPECT().resolveRequest(
			t.Context(),
			req,
			mock.Anything,
		).Return(false, nil)

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})
}
