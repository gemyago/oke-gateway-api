package app

import (
	"fmt"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client" // Import client for ObjectKey
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

func TestHTTPRouteController(t *testing.T) {
	newMockDeps := func(t *testing.T) HTTPRouteControllerDeps {
		return HTTPRouteControllerDeps{
			HTTPRouteModel:   NewMockhttpRouteModel(t),
			HTTPBackendModel: NewMockhttpBackendModel(t),
			RootLogger:       diag.RootTestLogger(),
		}
	}

	t.Run("Reconcile", func(t *testing.T) {
		t.Run("RelevantRoute", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(), // Use faker for random namespace
					Name:      fake.Lorem().Word(),      // Use faker for random name
				},
			}

			wantResolvedData := resolvedRouteDetails{
				httpRoute: makeRandomHTTPRoute(),
				gatewayDetails: resolvedGatewayDetails{
					gateway: *newRandomGateway(),
					config:  makeRandomGatewayConfig(),
				},
			}

			wantBackends := make(map[string]v1.Service)
			for range 3 {
				svc := makeRandomService()
				fullName := types.NamespacedName{
					Namespace: svc.Namespace,
					Name:      svc.Name,
				}
				wantBackends[fullName.String()] = svc
			}

			mockModel, _ := deps.HTTPRouteModel.(*MockhttpRouteModel)
			mockModel.EXPECT().resolveRequest(
				t.Context(),
				req,
			).Return(map[types.NamespacedName]resolvedRouteDetails{
				req.NamespacedName: wantResolvedData,
			}, (error)(nil))

			mockModel.EXPECT().isProgrammingRequired(wantResolvedData).Return(true, nil)

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
			).Return(wantBackends, nil)

			programmedPolicyRules := []string{
				"policy1-" + fake.Lorem().Word(),
				"policy2-" + fake.Lorem().Word(),
				"policy3-" + fake.Lorem().Word(),
			}

			mockModel.EXPECT().programRoute(
				t.Context(),
				programRouteParams{
					gateway:       wantResolvedData.gatewayDetails.gateway,
					config:        wantResolvedData.gatewayDetails.config,
					httpRoute:     wantAcceptedRoute,
					knownBackends: wantBackends,
				},
			).Return(programRouteResult{
				programmedPolicyRules: programmedPolicyRules,
			}, nil)

			mockModel.EXPECT().setProgrammed(
				t.Context(),
				setProgrammedParams{
					gatewayClass:          wantResolvedData.gatewayDetails.gatewayClass,
					gateway:               wantResolvedData.gatewayDetails.gateway,
					httpRoute:             wantAcceptedRoute,
					matchedRef:            wantResolvedData.matchedRef,
					programmedPolicyRules: programmedPolicyRules,
				},
			).Return(nil)

			mockBackendModel, _ := deps.HTTPBackendModel.(*MockhttpBackendModel)
			mockBackendModel.EXPECT().syncRouteEndpoints(
				t.Context(),
				syncRouteEndpointsParams{
					httpRoute: wantResolvedData.httpRoute,
					config:    wantResolvedData.gatewayDetails.config,
				},
			).Return(nil)

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("IrrelevantRoute", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(),
					Name:      fake.Lorem().Word(),
				},
			}

			mockModel, _ := deps.HTTPRouteModel.(*MockhttpRouteModel)
			mockModel.EXPECT().resolveRequest(
				t.Context(),
				req,
			).Return(map[types.NamespacedName]resolvedRouteDetails{}, (error)(nil))

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("RelevantRouteDeleted", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(),
					Name:      fake.Lorem().Word(),
				},
			}

			deletedRoute := makeRandomHTTPRoute()
			now := metav1.Now()
			deletedRoute.DeletionTimestamp = &now

			wantResolvedData := resolvedRouteDetails{
				httpRoute: deletedRoute,
				gatewayDetails: resolvedGatewayDetails{
					gateway: *newRandomGateway(),
					config:  makeRandomGatewayConfig(),
				},
			}

			mockModel, _ := deps.HTTPRouteModel.(*MockhttpRouteModel)
			mockModel.EXPECT().resolveRequest(
				t.Context(),
				req,
			).Return(map[types.NamespacedName]resolvedRouteDetails{
				req.NamespacedName: wantResolvedData,
			}, (error)(nil))

			mockModel.EXPECT().deprovisionRoute(
				t.Context(),
				deprovisionRouteParams{ // Assuming deprovisionRouteParams is the correct type
					gateway:          wantResolvedData.gatewayDetails.gateway,
					config:           wantResolvedData.gatewayDetails.config,
					httpRoute:        wantResolvedData.httpRoute,
					matchedListeners: wantResolvedData.matchedListeners,
				},
			).Return(nil)

			mockBackendModel, _ := deps.HTTPBackendModel.(*MockhttpBackendModel)

			result, err := controller.Reconcile(t.Context(), req)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
			mockModel.AssertNotCalled(t, "isProgrammingRequired", mock.Anything)
			mockModel.AssertNotCalled(t, "acceptRoute", mock.Anything, mock.Anything)
			mockModel.AssertNotCalled(t, "programRoute", mock.Anything, mock.Anything)
			mockModel.AssertNotCalled(t, "setProgrammed", mock.Anything, mock.Anything)
			mockBackendModel.AssertNotCalled(t, "syncRouteEndpoints", mock.Anything, mock.Anything)
		})

		t.Run("ResolveRequestError", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(),
					Name:      fake.Lorem().Word(),
				},
			}

			mockModel, _ := deps.HTTPRouteModel.(*MockhttpRouteModel)
			wantErr := fmt.Errorf("resolve error: %s", fake.Lorem().Sentence(10))
			mockModel.EXPECT().resolveRequest(
				t.Context(),
				req,
			).Return((map[types.NamespacedName]resolvedRouteDetails)(nil), wantErr)

			result, err := controller.Reconcile(t.Context(), req)

			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("AcceptRouteError", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(),
					Name:      fake.Lorem().Word(),
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
			).Return(map[types.NamespacedName]resolvedRouteDetails{
				req.NamespacedName: wantResolvedData,
			}, (error)(nil))

			mockModel.EXPECT().isProgrammingRequired(wantResolvedData).Return(true, nil)

			wantErr := fmt.Errorf("accept error: %s", fake.Lorem().Sentence(10))
			mockModel.EXPECT().acceptRoute(
				t.Context(),
				wantResolvedData,
			).Return(nil, wantErr)

			result, err := controller.Reconcile(t.Context(), req)

			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("ResolveBackendRefsError", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(),
					Name:      fake.Lorem().Word(),
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
			).Return(map[types.NamespacedName]resolvedRouteDetails{
				req.NamespacedName: wantResolvedData,
			}, (error)(nil))

			mockModel.EXPECT().isProgrammingRequired(wantResolvedData).Return(true, nil)

			wantAcceptedRoute := makeRandomHTTPRoute()
			mockModel.EXPECT().acceptRoute(
				t.Context(),
				wantResolvedData,
			).Return(&wantAcceptedRoute, nil)

			wantErr := fmt.Errorf("resolve backend refs error: %s", fake.Lorem().Sentence(10))
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
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(),
					Name:      fake.Lorem().Word(),
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
			).Return(map[types.NamespacedName]resolvedRouteDetails{
				req.NamespacedName: wantResolvedData,
			}, (error)(nil))

			mockModel.EXPECT().isProgrammingRequired(wantResolvedData).Return(true, nil)

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

			wantErr := fmt.Errorf("program route error: %s", fake.Lorem().Sentence(10))
			mockModel.EXPECT().programRoute(
				t.Context(),
				programRouteParams{
					gateway:       wantResolvedData.gatewayDetails.gateway,
					config:        wantResolvedData.gatewayDetails.config,
					httpRoute:     wantAcceptedRoute,
					knownBackends: wantBackendRefs,
				},
			).Return(programRouteResult{}, wantErr)

			result, err := controller.Reconcile(t.Context(), req)

			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("ProgrammingNotRequired", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(),
					Name:      fake.Lorem().Word(),
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
			).Return(map[types.NamespacedName]resolvedRouteDetails{
				req.NamespacedName: wantResolvedData,
			}, (error)(nil))

			mockModel.EXPECT().isProgrammingRequired(wantResolvedData).Return(false, nil)

			mockBackendModel, _ := deps.HTTPBackendModel.(*MockhttpBackendModel)
			mockBackendModel.EXPECT().syncRouteEndpoints(
				t.Context(),
				syncRouteEndpointsParams{
					httpRoute: wantResolvedData.httpRoute,
					config:    wantResolvedData.gatewayDetails.config,
				},
			).Return(nil)

			result, err := controller.Reconcile(t.Context(), req)

			mockModel.AssertNotCalled(t, "acceptRoute", mock.Anything, mock.Anything)
			mockModel.AssertNotCalled(t, "resolveBackendRefs", mock.Anything, mock.Anything)
			mockModel.AssertNotCalled(t, "programRoute", mock.Anything, mock.Anything)

			require.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("SetProgrammedError", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(), // Use faker for random namespace
					Name:      fake.Lorem().Word(),      // Use faker for random name
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
			).Return(map[types.NamespacedName]resolvedRouteDetails{
				req.NamespacedName: wantResolvedData,
			}, (error)(nil))

			mockModel.EXPECT().isProgrammingRequired(wantResolvedData).Return(true, nil)

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
					gateway:       wantResolvedData.gatewayDetails.gateway,
					config:        wantResolvedData.gatewayDetails.config,
					httpRoute:     wantAcceptedRoute,
					knownBackends: wantBackendRefs,
				},
			).Return(programRouteResult{}, nil)

			wantErr := fmt.Errorf("set programmed error: %s", fake.Lorem().Sentence(10))
			mockModel.EXPECT().setProgrammed(
				t.Context(),
				setProgrammedParams{
					gatewayClass: wantResolvedData.gatewayDetails.gatewayClass,
					gateway:      wantResolvedData.gatewayDetails.gateway,
					httpRoute:    wantAcceptedRoute,
					matchedRef:   wantResolvedData.matchedRef,
				},
			).Return(wantErr)

			result, err := controller.Reconcile(t.Context(), req)

			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("IsProgrammingRequiredError", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(),
					Name:      fake.Lorem().Word(),
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
			).Return(map[types.NamespacedName]resolvedRouteDetails{
				req.NamespacedName: wantResolvedData,
			}, (error)(nil))

			wantErr := fmt.Errorf("is programming required error: %s", fake.Lorem().Sentence(10))
			mockModel.EXPECT().isProgrammingRequired(wantResolvedData).Return(false, wantErr)

			result, err := controller.Reconcile(t.Context(), req)

			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("syncRouteEndpError", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(),
					Name:      fake.Lorem().Word(),
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
			).Return(map[types.NamespacedName]resolvedRouteDetails{
				req.NamespacedName: wantResolvedData,
			}, (error)(nil))

			// Assume programming is not required to isolate the sync error
			mockModel.EXPECT().isProgrammingRequired(wantResolvedData).Return(false, nil)

			mockBackendModel, _ := deps.HTTPBackendModel.(*MockhttpBackendModel)
			wantErr := fmt.Errorf("sync error: %s", fake.Lorem().Sentence(10))
			mockBackendModel.EXPECT().syncRouteEndpoints(
				t.Context(),
				syncRouteEndpointsParams{
					httpRoute: wantResolvedData.httpRoute,
					config:    wantResolvedData.gatewayDetails.config,
				},
			).Return(wantErr)

			result, err := controller.Reconcile(t.Context(), req)

			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
		})

		t.Run("deprovisionRouteError", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			controller := NewHTTPRouteController(deps)

			req := reconcile.Request{
				NamespacedName: client.ObjectKey{
					Namespace: fake.Internet().Domain(),
					Name:      fake.Lorem().Word(),
				},
			}

			deletedRoute := makeRandomHTTPRoute()
			now := metav1.Now()
			deletedRoute.DeletionTimestamp = &now

			wantResolvedData := resolvedRouteDetails{
				httpRoute: deletedRoute,
				gatewayDetails: resolvedGatewayDetails{
					gateway: *newRandomGateway(),
					config:  makeRandomGatewayConfig(),
				},
			}

			mockModel, _ := deps.HTTPRouteModel.(*MockhttpRouteModel)
			mockModel.EXPECT().resolveRequest(
				t.Context(),
				req,
			).Return(map[types.NamespacedName]resolvedRouteDetails{
				req.NamespacedName: wantResolvedData,
			}, (error)(nil))

			wantErr := fmt.Errorf("deprovision error: %s", fake.Lorem().Sentence(10))
			mockModel.EXPECT().deprovisionRoute(
				t.Context(),
				deprovisionRouteParams{ // Assuming deprovisionRouteParams is the correct type
					gateway:          wantResolvedData.gatewayDetails.gateway,
					config:           wantResolvedData.gatewayDetails.config,
					httpRoute:        wantResolvedData.httpRoute,
					matchedListeners: wantResolvedData.matchedListeners,
				},
			).Return(wantErr)

			result, err := controller.Reconcile(t.Context(), req)

			require.ErrorIs(t, err, wantErr)
			assert.Equal(t, reconcile.Result{}, result)
			mockModel.AssertNotCalled(t, "isProgrammingRequired", mock.Anything)
			mockModel.AssertNotCalled(t, "acceptRoute", mock.Anything, mock.Anything)
			mockModel.AssertNotCalled(t, "programRoute", mock.Anything, mock.Anything)
			mockModel.AssertNotCalled(t, "setProgrammed", mock.Anything, mock.Anything)
		})
	})
}
