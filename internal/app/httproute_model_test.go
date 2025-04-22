package app

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	k8sapi "github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
	"github.com/go-faker/faker/v4"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

		t.Run("no such route", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: faker.Word(),
					Name:      faker.Word(),
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().Get(t.Context(), req.NamespacedName, mock.Anything).Return(
				apierrors.NewNotFound(
					schema.GroupResource{Group: gatewayv1.GroupName, Resource: "HTTPRoute"},
					req.NamespacedName.String(),
				),
			)

			var receiver resolvedRouteDetails
			accepted, err := model.resolveRequest(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.False(t, accepted, "parent should not be resolved")
		})
	})

	t.Run("acceptRoute", func(t *testing.T) {
		t.Run("add new accepted parent", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			routeData := resolvedRouteDetails{
				gatewayDetails: acceptedGatewayDetails{
					gateway:      *newRandomGateway(),
					gatewayClass: *newRandomGatewayClass(),
				},
				httpRoute: makeRandomHTTPRoute(),
			}

			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().Status().Return(mockStatusWriter)

			var updatedRoute *gatewayv1.HTTPRoute
			mockStatusWriter.EXPECT().
				Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
					var ok bool
					updatedRoute, ok = obj.(*gatewayv1.HTTPRoute)
					return assert.True(t, ok)
				})).
				Return(nil)

			acceptedRoute, err := model.acceptRoute(t.Context(), routeData)

			require.NoError(t, err)
			assert.Same(t, acceptedRoute, updatedRoute)

			assert.Len(t, updatedRoute.Status.Parents, 1)

			acceptedParent := updatedRoute.Status.Parents[0]
			assert.Equal(t, routeData.matchedRef, acceptedParent.ParentRef)
			assert.Equal(t,
				routeData.gatewayDetails.gatewayClass.Spec.ControllerName,
				acceptedParent.ControllerName,
			)

			gotCondition := meta.FindStatusCondition(acceptedParent.Conditions, string(gatewayv1.RouteConditionAccepted))
			require.NotNil(t, gotCondition)
			assert.False(t, gotCondition.LastTransitionTime.IsZero())
			assert.Equal(t, &metav1.Condition{
				Type:               string(gatewayv1.RouteConditionAccepted),
				Status:             metav1.ConditionTrue,
				Reason:             string(gatewayv1.RouteReasonAccepted),
				ObservedGeneration: routeData.httpRoute.Generation,
				LastTransitionTime: gotCondition.LastTransitionTime,
				Message:            fmt.Sprintf("Route accepted by %s", routeData.gatewayDetails.gateway.Name),
			}, gotCondition)
		})

		t.Run("set condition of existing parent", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			gatewayClass := newRandomGatewayClass()
			existingParentStatus := makeRandomRouteParentStatus(
				func(s *gatewayv1.RouteParentStatus) {
					s.ControllerName = gatewayClass.Spec.ControllerName
				},
			)
			routeData := resolvedRouteDetails{
				gatewayDetails: acceptedGatewayDetails{
					gateway:      *newRandomGateway(),
					gatewayClass: *gatewayClass,
				},
				httpRoute: makeRandomHTTPRoute(
					func(h *gatewayv1.HTTPRoute) {
						h.Status.Parents = []gatewayv1.RouteParentStatus{
							makeRandomRouteParentStatus(),
							makeRandomRouteParentStatus(),
							existingParentStatus,
							makeRandomRouteParentStatus(),
						}
					},
				),
			}

			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().Status().Return(mockStatusWriter)

			var updatedRoute *gatewayv1.HTTPRoute
			mockStatusWriter.EXPECT().
				Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
					var ok bool
					updatedRoute, ok = obj.(*gatewayv1.HTTPRoute)
					return assert.True(t, ok)
				})).
				Return(nil)

			acceptedRoute, err := model.acceptRoute(t.Context(), routeData)
			require.NoError(t, err)
			assert.Same(t, acceptedRoute, updatedRoute)

			assert.Len(t, updatedRoute.Status.Parents, 4)

			acceptedParent, found := lo.Find(updatedRoute.Status.Parents, func(s gatewayv1.RouteParentStatus) bool {
				return s.ControllerName == gatewayClass.Spec.ControllerName
			})
			require.True(t, found)

			gotCondition := meta.FindStatusCondition(acceptedParent.Conditions, string(gatewayv1.RouteConditionAccepted))
			require.NotNil(t, gotCondition)
			assert.False(t, gotCondition.LastTransitionTime.IsZero())
			assert.Equal(t, &metav1.Condition{
				Type:               string(gatewayv1.RouteConditionAccepted),
				Status:             metav1.ConditionTrue,
				Reason:             string(gatewayv1.RouteReasonAccepted),
				ObservedGeneration: routeData.httpRoute.Generation,
				LastTransitionTime: gotCondition.LastTransitionTime,
				Message:            fmt.Sprintf("Route accepted by %s", routeData.gatewayDetails.gateway.Name),
			}, gotCondition)
		})
		t.Run("should not update if already accepted", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			gatewayClass := newRandomGatewayClass()
			routeGeneration := rand.Int64N(1000000)
			existingParentStatus := makeRandomRouteParentStatus(
				func(s *gatewayv1.RouteParentStatus) {
					s.ControllerName = gatewayClass.Spec.ControllerName
					meta.SetStatusCondition(&s.Conditions, metav1.Condition{
						Type:               string(gatewayv1.RouteConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             string(gatewayv1.RouteReasonAccepted),
						ObservedGeneration: routeGeneration,
					})
				},
			)
			routeData := resolvedRouteDetails{
				gatewayDetails: acceptedGatewayDetails{
					gateway:      *newRandomGateway(),
					gatewayClass: *gatewayClass,
				},
				httpRoute: makeRandomHTTPRoute(
					func(h *gatewayv1.HTTPRoute) {
						h.Generation = routeGeneration
						h.Status.Parents = []gatewayv1.RouteParentStatus{
							makeRandomRouteParentStatus(),
							makeRandomRouteParentStatus(),
							existingParentStatus,
							makeRandomRouteParentStatus(),
						}
					},
				),
			}

			acceptedRoute, err := model.acceptRoute(t.Context(), routeData)
			require.NoError(t, err)
			assert.Equal(t, acceptedRoute, &routeData.httpRoute)
		})
		t.Run("should update if generation mismatch", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			gatewayClass := newRandomGatewayClass()
			routeGeneration := rand.Int64N(1000000)
			existingParentStatus := makeRandomRouteParentStatus(
				func(s *gatewayv1.RouteParentStatus) {
					s.ControllerName = gatewayClass.Spec.ControllerName
					meta.SetStatusCondition(&s.Conditions, metav1.Condition{
						Type:               string(gatewayv1.RouteConditionAccepted),
						Status:             metav1.ConditionTrue,
						Reason:             string(gatewayv1.RouteReasonAccepted),
						ObservedGeneration: routeGeneration - 1,
					})
				},
			)
			routeData := resolvedRouteDetails{
				gatewayDetails: acceptedGatewayDetails{
					gateway:      *newRandomGateway(),
					gatewayClass: *gatewayClass,
				},
				httpRoute: makeRandomHTTPRoute(
					func(h *gatewayv1.HTTPRoute) {
						h.Generation = routeGeneration
						h.Status.Parents = []gatewayv1.RouteParentStatus{
							makeRandomRouteParentStatus(),
							makeRandomRouteParentStatus(),
							existingParentStatus,
							makeRandomRouteParentStatus(),
						}
					},
				),
			}

			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().Status().Return(mockStatusWriter)

			var updatedRoute *gatewayv1.HTTPRoute
			mockStatusWriter.EXPECT().
				Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
					var ok bool
					updatedRoute, ok = obj.(*gatewayv1.HTTPRoute)
					return assert.True(t, ok)
				})).
				Return(nil)

			acceptedRoute, err := model.acceptRoute(t.Context(), routeData)
			require.NoError(t, err)
			assert.Same(t, acceptedRoute, updatedRoute)

			acceptedParent, found := lo.Find(updatedRoute.Status.Parents, func(s gatewayv1.RouteParentStatus) bool {
				return s.ControllerName == gatewayClass.Spec.ControllerName
			})
			require.True(t, found)

			gotCondition := meta.FindStatusCondition(acceptedParent.Conditions, string(gatewayv1.RouteConditionAccepted))
			require.NotNil(t, gotCondition)
			assert.Equal(t, routeData.httpRoute.Generation, gotCondition.ObservedGeneration)
			assert.Equal(t, metav1.ConditionTrue, gotCondition.Status)
			assert.Equal(t, string(gatewayv1.RouteReasonAccepted), gotCondition.Reason)
		})
	})
}
