package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestHTTPRouteModelImpl(t *testing.T) {
	newMockDeps := func(t *testing.T) httpRouteModelDeps {
		return httpRouteModelDeps{
			K8sClient:    NewMockk8sClient(t),
			RootLogger:   diag.RootTestLogger(),
			GatewayModel: NewMockgatewayModel(t),
		}
	}

	setupClientGet := func(
		t *testing.T,
		cl k8sClient,
		wantName types.NamespacedName,
		wantObj interface{},
	) {
		mockK8sClient, _ := cl.(*Mockk8sClient)
		mockK8sClient.EXPECT().Get(
			t.Context(),
			wantName,
			mock.Anything,
		).RunAndReturn(func(
			_ context.Context,
			name types.NamespacedName,
			obj client.Object,
			_ ...client.GetOption,
		) error {
			assert.Equal(t, wantName, name)
			reflect.ValueOf(obj).Elem().Set(reflect.ValueOf(wantObj))
			return nil
		})
	}

	t.Run("resolveRequest", func(t *testing.T) {
		t.Run("relevant parent", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: faker.Word(),
					Name:      faker.Word(),
				},
			}
			otherRef1 := makeRandomParentRef()
			otherRef2 := makeRandomParentRef()
			workingRef := makeRandomParentRef()

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefOpt(otherRef1),
				randomHTTPRouteWithRandomParentRefOpt(otherRef2),
				randomHTTPRouteWithRandomParentRefOpt(workingRef),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			gatewayModel.EXPECT().acceptReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef1.Namespace)),
						Name:      string(otherRef1.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			gatewayModel.EXPECT().acceptReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef2.Namespace)),
						Name:      string(otherRef2.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			gatewayData := makeRandomAcceptedGatewayDetails()

			gatewayModel.EXPECT().acceptReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(workingRef.Namespace)),
						Name:      string(workingRef.Name),
					},
				},
				mock.Anything,
			).RunAndReturn(func(
				_ context.Context,
				_ reconcile.Request,
				receiver *acceptedGatewayDetails,
			) (bool, error) {
				*receiver = *gatewayData
				return true, nil
			})

			var receiver resolvedRouteDetails
			accepted, err := model.resolveRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.True(t, accepted, "parent should be resolved")

			assert.Equal(t, route, receiver.httpRoute)
			assert.Equal(t, workingRef, receiver.matchedRef)
			assert.Equal(t, *gatewayData, receiver.gatewayDetails)
		})

		t.Run("default namespace", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: faker.Word(),
					Name:      faker.Word(),
				},
			}
			workingRef := makeRandomParentRef()
			workingRef.Namespace = nil

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefOpt(workingRef),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			gatewayData := makeRandomAcceptedGatewayDetails()

			gatewayModel.EXPECT().acceptReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: req.NamespacedName.Namespace,
						Name:      string(workingRef.Name),
					},
				},
				mock.Anything,
			).RunAndReturn(func(
				_ context.Context,
				_ reconcile.Request,
				receiver *acceptedGatewayDetails,
			) (bool, error) {
				*receiver = *gatewayData
				return true, nil
			})

			var receiver resolvedRouteDetails
			accepted, err := model.resolveRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.True(t, accepted, "parent should be resolved")

			assert.Equal(t, route, receiver.httpRoute)
			assert.Equal(t, workingRef, receiver.matchedRef)
			assert.Equal(t, *gatewayData, receiver.gatewayDetails)
		})

		t.Run("no relevant parent", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: faker.Word(),
					Name:      faker.Word(),
				},
			}
			otherRef1 := makeRandomParentRef()
			otherRef2 := makeRandomParentRef()

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefOpt(otherRef1),
				randomHTTPRouteWithRandomParentRefOpt(otherRef2),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			gatewayModel.EXPECT().acceptReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef1.Namespace)),
						Name:      string(otherRef1.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			gatewayModel.EXPECT().acceptReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef2.Namespace)),
						Name:      string(otherRef2.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			var receiver resolvedRouteDetails
			accepted, err := model.resolveRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.False(t, accepted, "parent should not be resolved")
		})
	})

	t.Run("acceptRoute", func(t *testing.T) {
		t.Run("transition to accepted", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			route := makeRandomHTTPRoute()
			routeData := resolvedRouteDetails{
				gatewayDetails: acceptedGatewayDetails{
					gateway:      *newRandomGateway(),
					gatewayClass: *newRandomGatewayClass(),
				},
				httpRoute: route,
			}

			err := model.acceptRoute(t.Context(), &routeData)
			require.NoError(t, err)

			acceptedParent := route.Status.Parents[0]

			assert.Equal(t, routeData.matchedRef, acceptedParent.ParentRef)
			assert.Equal(t, routeData.gatewayDetails.gatewayClass.Spec.ControllerName, acceptedParent.ControllerName)
			assert.Equal(t, gatewayv1.RouteConditionAccepted, acceptedParent.Conditions[0].Type)
			assert.Equal(t, gatewayv1.RouteReasonAccepted, acceptedParent.Conditions[0].Reason)
			assert.Equal(t, metav1.ConditionTrue, acceptedParent.Conditions[0].Status)
		})
	})
}
