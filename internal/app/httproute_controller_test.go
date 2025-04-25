package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	types "k8s.io/apimachinery/pkg/types"
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
			gatewayDetails: resolvedGatewayDetails{
				gateway: *newRandomGateway(),
				config:  makeRandomGatewayConfig(),
			},
		}

		wantBackendRefs := make(map[string]v1.Service)
		for range 3 {
			svc := makeRandomService()
			fullName := types.NamespacedName{
				Namespace: svc.Namespace,
				Name:      svc.Name,
			}
			wantBackendRefs[fullName.String()] = svc
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

		mockModel.EXPECT().isProgrammingRequired(t.Context(), wantResolvedData).Return(true, nil)

		wantAcceptedRoute := makeRandomHTTPRoute()

		mockModel.EXPECT().acceptRoute(
			t.Context(),
			wantResolvedData,
		).Return(&wantAcceptedRoute, nil)

		mockModel.EXPECT().resolveBackendRefs(
			t.Context(),
			resolveBackendRefsParams{
				httpRoute: wantAcceptedRoute,
			},
		).Return(wantBackendRefs, nil)

		mockModel.EXPECT().programRoute(
			t.Context(),
			programRouteParams{
				gateway:             wantResolvedData.gatewayDetails.gateway,
				config:              wantResolvedData.gatewayDetails.config,
				httpRoute:           wantAcceptedRoute,
				resolvedBackendRefs: wantBackendRefs,
			},
		).Return(nil)

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

	t.Run("ResolveRequestError", func(t *testing.T) {
		deps := newMockDeps(t)
		controller := NewHTTPRouteController(deps)

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: faker.DomainName(),
				Name:      faker.Word(),
			},
		}

		mockModel, _ := deps.HTTPRouteModel.(*MockhttpRouteModel)
		wantErr := fmt.Errorf("resolve error: %s", faker.Sentence())
		mockModel.EXPECT().resolveRequest(
			t.Context(),
			req,
			mock.Anything,
		).Return(false, wantErr)

		result, err := controller.Reconcile(t.Context(), req)

		require.ErrorIs(t, err, wantErr)
		assert.Equal(t, reconcile.Result{}, result)
	})

	t.Run("AcceptRouteError", func(t *testing.T) {
		deps := newMockDeps(t)
		controller := NewHTTPRouteController(deps)

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: faker.DomainName(),
				Name:      faker.Word(),
			},
		}

		wantResolvedData := resolvedRouteDetails{
			httpRoute: makeRandomHTTPRoute(),
			gatewayDetails: resolvedGatewayDetails{
				gateway: *newRandomGateway(),
				config:  makeRandomGatewayConfig(),
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

		mockModel.EXPECT().isProgrammingRequired(t.Context(), wantResolvedData).Return(true, nil)

		wantErr := fmt.Errorf("accept error: %s", faker.Sentence())
		mockModel.EXPECT().acceptRoute(
			t.Context(),
			wantResolvedData,
		).Return(nil, wantErr)

		result, err := controller.Reconcile(t.Context(), req)

		require.ErrorIs(t, err, wantErr)
		assert.Equal(t, reconcile.Result{}, result)
	})

	t.Run("ResolveBackendRefsError", func(t *testing.T) {
		deps := newMockDeps(t)
		controller := NewHTTPRouteController(deps)

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: faker.DomainName(),
				Name:      faker.Word(),
			},
		}

		wantResolvedData := resolvedRouteDetails{
			httpRoute: makeRandomHTTPRoute(),
			gatewayDetails: resolvedGatewayDetails{
				gateway: *newRandomGateway(),
				config:  makeRandomGatewayConfig(),
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

		mockModel.EXPECT().isProgrammingRequired(t.Context(), wantResolvedData).Return(true, nil)

		wantAcceptedRoute := makeRandomHTTPRoute()
		mockModel.EXPECT().acceptRoute(
			t.Context(),
			wantResolvedData,
		).Return(&wantAcceptedRoute, nil)

		wantErr := fmt.Errorf("resolve backend refs error: %s", faker.Sentence())
		mockModel.EXPECT().resolveBackendRefs(
			t.Context(),
			resolveBackendRefsParams{
				httpRoute: wantAcceptedRoute,
			},
		).Return(nil, wantErr)

		result, err := controller.Reconcile(t.Context(), req)

		require.ErrorIs(t, err, wantErr)
		assert.Equal(t, reconcile.Result{}, result)
	})

	t.Run("ProgramRouteError", func(t *testing.T) {
		deps := newMockDeps(t)
		controller := NewHTTPRouteController(deps)

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: faker.DomainName(),
				Name:      faker.Word(),
			},
		}

		wantResolvedData := resolvedRouteDetails{
			httpRoute: makeRandomHTTPRoute(),
			gatewayDetails: resolvedGatewayDetails{
				gateway: *newRandomGateway(),
				config:  makeRandomGatewayConfig(),
			},
		}

		wantBackendRefs := make(map[string]v1.Service)
		for range 3 {
			svc := makeRandomService()
			fullName := types.NamespacedName{
				Namespace: svc.Namespace,
				Name:      svc.Name,
			}
			wantBackendRefs[fullName.String()] = svc
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

		mockModel.EXPECT().isProgrammingRequired(t.Context(), wantResolvedData).Return(true, nil)

		wantAcceptedRoute := makeRandomHTTPRoute()
		mockModel.EXPECT().acceptRoute(
			t.Context(),
			wantResolvedData,
		).Return(&wantAcceptedRoute, nil)

		mockModel.EXPECT().resolveBackendRefs(
			t.Context(),
			resolveBackendRefsParams{
				httpRoute: wantAcceptedRoute,
			},
		).Return(wantBackendRefs, nil)

		wantErr := fmt.Errorf("program route error: %s", faker.Sentence())
		mockModel.EXPECT().programRoute(
			t.Context(),
			programRouteParams{
				gateway:             wantResolvedData.gatewayDetails.gateway,
				config:              wantResolvedData.gatewayDetails.config,
				httpRoute:           wantAcceptedRoute,
				resolvedBackendRefs: wantBackendRefs,
			},
		).Return(wantErr)

		result, err := controller.Reconcile(t.Context(), req)

		require.ErrorIs(t, err, wantErr)
		assert.Equal(t, reconcile.Result{}, result)
	})

	t.Run("ProgrammingNotRequired", func(t *testing.T) {
		deps := newMockDeps(t)
		controller := NewHTTPRouteController(deps)

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: faker.DomainName(),
				Name:      faker.Word(),
			},
		}

		wantResolvedData := resolvedRouteDetails{
			httpRoute: makeRandomHTTPRoute(),
			gatewayDetails: resolvedGatewayDetails{
				gateway: *newRandomGateway(),
				config:  makeRandomGatewayConfig(),
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

		mockModel.EXPECT().isProgrammingRequired(t.Context(), wantResolvedData).Return(false, nil)

		result, err := controller.Reconcile(t.Context(), req)

		mockModel.AssertNotCalled(t, "acceptRoute", mock.Anything, mock.Anything)
		mockModel.AssertNotCalled(t, "resolveBackendRefs", mock.Anything, mock.Anything)
		mockModel.AssertNotCalled(t, "programRoute", mock.Anything, mock.Anything)

		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})
}
