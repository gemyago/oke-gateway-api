package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

type fakeGRPCRouteModel struct {
	resolveRequestFunc      func(context.Context, reconcile.Request) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error)
	acceptRouteFunc         func(context.Context, resolvedGRPCRouteDetails) (*gatewayv1.GRPCRoute, error)
	resolveBackendRefsFunc  func(context.Context, resolveGRPCBackendRefsParams) (map[string]corev1.Service, error)
	isProgrammingRequiredFn func(resolvedGRPCRouteDetails) bool
	ensureProtocolFunc      func(context.Context, ensureGRPCListenersProtocolParams) error
	programRouteFunc        func(context.Context, programGRPCRouteParams) (programGRPCRouteResult, error)
	deprovisionRouteFunc    func(context.Context, deprovisionGRPCRouteParams) error
	setProgrammedFunc       func(context.Context, setGRPCRouteProgrammedParams) error
}

func (m fakeGRPCRouteModel) resolveRequest(
	ctx context.Context,
	req reconcile.Request,
) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
	return m.resolveRequestFunc(ctx, req)
}

func (m fakeGRPCRouteModel) acceptRoute(
	ctx context.Context,
	details resolvedGRPCRouteDetails,
) (*gatewayv1.GRPCRoute, error) {
	if m.acceptRouteFunc == nil {
		return &details.grpcRoute, nil
	}
	return m.acceptRouteFunc(ctx, details)
}

func (m fakeGRPCRouteModel) resolveBackendRefs(
	ctx context.Context,
	params resolveGRPCBackendRefsParams,
) (map[string]corev1.Service, error) {
	return m.resolveBackendRefsFunc(ctx, params)
}

func (m fakeGRPCRouteModel) isProgrammingRequired(details resolvedGRPCRouteDetails) bool {
	return m.isProgrammingRequiredFn(details)
}

func (m fakeGRPCRouteModel) ensureGRPCListenersProtocol(
	ctx context.Context,
	params ensureGRPCListenersProtocolParams,
) error {
	if m.ensureProtocolFunc == nil {
		return nil
	}
	return m.ensureProtocolFunc(ctx, params)
}

func (m fakeGRPCRouteModel) programRoute(
	ctx context.Context,
	params programGRPCRouteParams,
) (programGRPCRouteResult, error) {
	return m.programRouteFunc(ctx, params)
}

func (m fakeGRPCRouteModel) deprovisionRoute(
	ctx context.Context,
	params deprovisionGRPCRouteParams,
) error {
	return m.deprovisionRouteFunc(ctx, params)
}

func (m fakeGRPCRouteModel) setProgrammed(
	ctx context.Context,
	params setGRPCRouteProgrammedParams,
) error {
	return m.setProgrammedFunc(ctx, params)
}

func TestGRPCRouteController(t *testing.T) {
	makeRoute := func() gatewayv1.GRPCRoute {
		fake := faker.New()
		return gatewayv1.GRPCRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns-" + fake.Lorem().Word(),
				Name:      "grpc-" + fake.Lorem().Word(),
			},
		}
	}
	makeResolved := func(route gatewayv1.GRPCRoute) resolvedGRPCRouteDetails {
		fake := faker.New()
		gateway := gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{
			Namespace: route.Namespace,
			Name:      "gw-" + fake.Lorem().Word(),
		}}
		gatewayClass := gatewayv1.GatewayClass{
			Spec: gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
		}
		return resolvedGRPCRouteDetails{
			grpcRoute: route,
			gatewayDetails: resolvedGatewayDetails{
				gateway:      gateway,
				gatewayClass: gatewayClass,
				config:       makeRandomGatewayConfig(),
			},
			matchedRef: gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gateway.Name)},
			matchedListeners: []gatewayv1.Listener{
				{Name: "grpc", Port: 50051},
			},
		}
	}
	newController := func(routeModel grpcRouteModel, backendModel httpBackendModel) *GRPCRouteController {
		return NewGRPCRouteController(GRPCRouteControllerDeps{
			RootLogger:       diag.RootTestLogger(),
			GRPCRouteModel:   routeModel,
			HTTPBackendModel: backendModel,
			DriftInterval:    2 * time.Minute,
		})
	}
	resolvedMap := func(route gatewayv1.GRPCRoute, resolved resolvedGRPCRouteDetails) map[apitypes.NamespacedName]resolvedGRPCRouteDetails {
		return map[apitypes.NamespacedName]resolvedGRPCRouteDetails{
			{Namespace: route.Namespace, Name: "gw"}: resolved,
		}
	}

	t.Run("programs route and syncs endpoints", func(t *testing.T) {
		route := makeRoute()
		resolved := makeResolved(route)
		service := corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: route.Namespace, Name: "grpc-svc"}}
		backendModel := NewMockhttpBackendModel(t)
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(
				_ context.Context,
				_ reconcile.Request,
			) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return resolvedMap(route, resolved), nil
			},
			isProgrammingRequiredFn: func(resolvedGRPCRouteDetails) bool { return true },
			acceptRouteFunc: func(_ context.Context, details resolvedGRPCRouteDetails) (*gatewayv1.GRPCRoute, error) {
				return &details.grpcRoute, nil
			},
			resolveBackendRefsFunc: func(_ context.Context, params resolveGRPCBackendRefsParams) (map[string]corev1.Service, error) {
				assert.Equal(t, route.Name, params.grpcRoute.Name)
				return map[string]corev1.Service{service.Namespace + "/" + service.Name: service}, nil
			},
			programRouteFunc: func(_ context.Context, params programGRPCRouteParams) (programGRPCRouteResult, error) {
				assert.Equal(t, route.Name, params.grpcRoute.Name)
				assert.Equal(t, service, params.knownBackends[service.Namespace+"/"+service.Name])
				return programGRPCRouteResult{programmedPolicyRules: []string{"grpc/rule"}}, nil
			},
			setProgrammedFunc: func(_ context.Context, params setGRPCRouteProgrammedParams) error {
				assert.Equal(t, []string{"grpc/rule"}, params.programmedPolicyRules)
				return nil
			},
		}
		backendModel.EXPECT().syncGRPCRouteEndpoints(t.Context(), syncGRPCRouteEndpointsParams{
			grpcRoute: route,
			config:    resolved.gatewayDetails.config,
		}).Return(nil).Once()

		got, err := newController(routeModel, backendModel).Reconcile(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: route.Namespace, Name: route.Name},
		})

		require.NoError(t, err)
		assert.GreaterOrEqual(t, got.RequeueAfter, 2*time.Minute)
		assert.LessOrEqual(t, got.RequeueAfter, 2*time.Minute+(2*time.Minute)/maxDriftRequeueJitterRatio)
	})

	t.Run("syncs endpoints when programming is not required", func(t *testing.T) {
		route := makeRoute()
		resolved := makeResolved(route)
		backendModel := NewMockhttpBackendModel(t)
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(
				_ context.Context,
				_ reconcile.Request,
			) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return resolvedMap(route, resolved), nil
			},
			isProgrammingRequiredFn: func(resolvedGRPCRouteDetails) bool { return false },
			ensureProtocolFunc: func(_ context.Context, params ensureGRPCListenersProtocolParams) error {
				assert.Equal(t, resolved.gatewayDetails.config.Spec.LoadBalancerID, params.config.Spec.LoadBalancerID)
				assert.Equal(t, resolved.matchedListeners, params.matchedListeners)
				return nil
			},
		}
		backendModel.EXPECT().syncGRPCRouteEndpoints(t.Context(), syncGRPCRouteEndpointsParams{
			grpcRoute: route,
			config:    resolved.gatewayDetails.config,
		}).Return(nil).Once()

		_, err := newController(routeModel, backendModel).Reconcile(t.Context(), reconcile.Request{})

		require.NoError(t, err)
	})

	t.Run("returns listener protocol errors", func(t *testing.T) {
		route := makeRoute()
		resolved := makeResolved(route)
		wantErr := errors.New(faker.New().Lorem().Sentence(10))
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(
				_ context.Context,
				_ reconcile.Request,
			) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return resolvedMap(route, resolved), nil
			},
			acceptRouteFunc: func(_ context.Context, details resolvedGRPCRouteDetails) (*gatewayv1.GRPCRoute, error) {
				return &details.grpcRoute, nil
			},
			ensureProtocolFunc: func(context.Context, ensureGRPCListenersProtocolParams) error {
				return wantErr
			},
		}

		_, err := newController(routeModel, NewMockhttpBackendModel(t)).Reconcile(t.Context(), reconcile.Request{})

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("returns programming errors", func(t *testing.T) {
		route := makeRoute()
		resolved := makeResolved(route)
		wantErr := errors.New(faker.New().Lorem().Sentence(10))
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(
				_ context.Context,
				_ reconcile.Request,
			) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return resolvedMap(route, resolved), nil
			},
			isProgrammingRequiredFn: func(resolvedGRPCRouteDetails) bool { return true },
			acceptRouteFunc: func(_ context.Context, details resolvedGRPCRouteDetails) (*gatewayv1.GRPCRoute, error) {
				return &details.grpcRoute, nil
			},
			resolveBackendRefsFunc: func(context.Context, resolveGRPCBackendRefsParams) (map[string]corev1.Service, error) {
				return map[string]corev1.Service{}, nil
			},
			programRouteFunc: func(context.Context, programGRPCRouteParams) (programGRPCRouteResult, error) {
				return programGRPCRouteResult{}, wantErr
			},
		}

		_, err := newController(routeModel, NewMockhttpBackendModel(t)).Reconcile(t.Context(), reconcile.Request{})

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("returns accept route errors", func(t *testing.T) {
		route := makeRoute()
		resolved := makeResolved(route)
		wantErr := errors.New(faker.New().Lorem().Sentence(10))
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(
				_ context.Context,
				_ reconcile.Request,
			) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return resolvedMap(route, resolved), nil
			},
			acceptRouteFunc: func(context.Context, resolvedGRPCRouteDetails) (*gatewayv1.GRPCRoute, error) {
				return nil, wantErr
			},
		}

		_, err := newController(routeModel, NewMockhttpBackendModel(t)).Reconcile(t.Context(), reconcile.Request{})

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("skips programming and endpoint sync when route is rejected", func(t *testing.T) {
		route := makeRoute()
		resolved := makeResolved(route)
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(
				_ context.Context,
				_ reconcile.Request,
			) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return resolvedMap(route, resolved), nil
			},
			acceptRouteFunc: func(context.Context, resolvedGRPCRouteDetails) (*gatewayv1.GRPCRoute, error) {
				var rejectedRoute *gatewayv1.GRPCRoute
				return rejectedRoute, nil
			},
		}

		got, err := newController(routeModel, NewMockhttpBackendModel(t)).Reconcile(t.Context(), reconcile.Request{})

		require.NoError(t, err)
		assert.GreaterOrEqual(t, got.RequeueAfter, 2*time.Minute)
		assert.LessOrEqual(t, got.RequeueAfter, 2*time.Minute+(2*time.Minute)/maxDriftRequeueJitterRatio)
	})

	t.Run("returns backend ref resolution errors", func(t *testing.T) {
		route := makeRoute()
		resolved := makeResolved(route)
		wantErr := errors.New(faker.New().Lorem().Sentence(10))
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(
				_ context.Context,
				_ reconcile.Request,
			) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return resolvedMap(route, resolved), nil
			},
			isProgrammingRequiredFn: func(resolvedGRPCRouteDetails) bool { return true },
			acceptRouteFunc: func(_ context.Context, details resolvedGRPCRouteDetails) (*gatewayv1.GRPCRoute, error) {
				return &details.grpcRoute, nil
			},
			resolveBackendRefsFunc: func(context.Context, resolveGRPCBackendRefsParams) (map[string]corev1.Service, error) {
				return nil, wantErr
			},
		}

		_, err := newController(routeModel, NewMockhttpBackendModel(t)).Reconcile(t.Context(), reconcile.Request{})

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("deprovisions deleted routes", func(t *testing.T) {
		route := makeRoute()
		now := metav1.Now()
		route.DeletionTimestamp = &now
		resolved := makeResolved(route)
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(context.Context, reconcile.Request) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return resolvedMap(route, resolved), nil
			},
			deprovisionRouteFunc: func(_ context.Context, params deprovisionGRPCRouteParams) error {
				assert.Equal(t, route.Name, params.grpcRoute.Name)
				assert.Equal(t, resolved.gatewayDetails.config, params.config)
				return nil
			},
		}

		_, err := newController(routeModel, NewMockhttpBackendModel(t)).Reconcile(t.Context(), reconcile.Request{})

		require.NoError(t, err)
	})

	t.Run("returns deprovision errors", func(t *testing.T) {
		route := makeRoute()
		now := metav1.Now()
		route.DeletionTimestamp = &now
		resolved := makeResolved(route)
		wantErr := errors.New(faker.New().Lorem().Sentence(10))
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(context.Context, reconcile.Request) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return resolvedMap(route, resolved), nil
			},
			deprovisionRouteFunc: func(context.Context, deprovisionGRPCRouteParams) error {
				return wantErr
			},
		}

		_, err := newController(routeModel, NewMockhttpBackendModel(t)).Reconcile(t.Context(), reconcile.Request{})

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("returns resolve request errors", func(t *testing.T) {
		wantErr := errors.New(faker.New().Lorem().Sentence(10))
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(context.Context, reconcile.Request) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return nil, wantErr
			},
		}

		_, err := newController(routeModel, NewMockhttpBackendModel(t)).Reconcile(t.Context(), reconcile.Request{})

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("returns no requeue when no routes resolve", func(t *testing.T) {
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(context.Context, reconcile.Request) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return map[apitypes.NamespacedName]resolvedGRPCRouteDetails{}, nil
			},
		}

		got, err := newController(routeModel, NewMockhttpBackendModel(t)).Reconcile(t.Context(), reconcile.Request{})

		require.NoError(t, err)
		assert.Zero(t, got.RequeueAfter)
	})

	t.Run("returns backend sync errors", func(t *testing.T) {
		route := makeRoute()
		resolved := makeResolved(route)
		wantErr := errors.New(faker.New().Lorem().Sentence(10))
		backendModel := NewMockhttpBackendModel(t)
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(context.Context, reconcile.Request) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return resolvedMap(route, resolved), nil
			},
			isProgrammingRequiredFn: func(resolvedGRPCRouteDetails) bool { return false },
		}
		backendModel.EXPECT().
			syncGRPCRouteEndpoints(t.Context(), mock.Anything).
			Return(wantErr).
			Once()

		_, err := newController(routeModel, backendModel).Reconcile(t.Context(), reconcile.Request{})

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("returns set programmed errors", func(t *testing.T) {
		route := makeRoute()
		resolved := makeResolved(route)
		wantErr := errors.New(faker.New().Lorem().Sentence(10))
		routeModel := fakeGRPCRouteModel{
			resolveRequestFunc: func(context.Context, reconcile.Request) (map[apitypes.NamespacedName]resolvedGRPCRouteDetails, error) {
				return resolvedMap(route, resolved), nil
			},
			isProgrammingRequiredFn: func(resolvedGRPCRouteDetails) bool { return true },
			acceptRouteFunc: func(_ context.Context, details resolvedGRPCRouteDetails) (*gatewayv1.GRPCRoute, error) {
				return &details.grpcRoute, nil
			},
			resolveBackendRefsFunc: func(context.Context, resolveGRPCBackendRefsParams) (map[string]corev1.Service, error) {
				return map[string]corev1.Service{}, nil
			},
			programRouteFunc: func(context.Context, programGRPCRouteParams) (programGRPCRouteResult, error) {
				return programGRPCRouteResult{}, nil
			},
			setProgrammedFunc: func(context.Context, setGRPCRouteProgrammedParams) error {
				return wantErr
			},
		}

		_, err := newController(routeModel, NewMockhttpBackendModel(t)).Reconcile(t.Context(), reconcile.Request{})

		require.ErrorIs(t, err, wantErr)
	})
}
