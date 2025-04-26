package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	k8sapi "github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
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
			OciLBModel:   NewMockociLoadBalancerModel(t),
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

			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef1.Namespace)),
						Name:      string(otherRef1.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			gatewayModel.EXPECT().resolveReconcileRequest(
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

			gatewayModel.EXPECT().resolveReconcileRequest(
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
				receiver *resolvedGatewayDetails,
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

			gatewayModel.EXPECT().resolveReconcileRequest(
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
				receiver *resolvedGatewayDetails,
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

			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef1.Namespace)),
						Name:      string(otherRef1.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			gatewayModel.EXPECT().resolveReconcileRequest(
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

		t.Run("client get error", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: faker.Word(),
					Name:      faker.Word(),
				},
			}
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			expectedErr := errors.New(faker.Sentence())
			mockK8sClient.EXPECT().Get(t.Context(), req.NamespacedName, mock.Anything).Return(expectedErr)

			var receiver resolvedRouteDetails
			accepted, err := model.resolveRequest(t.Context(), req, &receiver)

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
			assert.False(t, accepted, "should not be accepted on error")
		})

		t.Run("gateway resolve error", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: faker.Word(),
					Name:      faker.Word(),
				},
			}
			workingRef := makeRandomParentRef()
			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefOpt(workingRef),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)
			expectedErr := errors.New(faker.Sentence())
			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(workingRef.Namespace)),
						Name:      string(workingRef.Name),
					},
				},
				mock.Anything,
			).Return(false, expectedErr)

			var receiver resolvedRouteDetails
			accepted, err := model.resolveRequest(t.Context(), req, &receiver)

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
			assert.False(t, accepted, "should not be accepted on error")
		})
	})

	t.Run("acceptRoute", func(t *testing.T) {
		t.Run("add new accepted parent", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			routeData := resolvedRouteDetails{
				gatewayDetails: resolvedGatewayDetails{
					gateway: *newRandomGateway(),
					gatewayClass: *newRandomGatewayClass(
						randomGatewayClassWithControllerNameOpt(gatewayv1.GatewayController(faker.Word())),
					),
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
				gatewayDetails: resolvedGatewayDetails{
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
				gatewayDetails: resolvedGatewayDetails{
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
				gatewayDetails: resolvedGatewayDetails{
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

		t.Run("client status update error", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			routeData := resolvedRouteDetails{
				gatewayDetails: resolvedGatewayDetails{
					gateway:      *newRandomGateway(),
					gatewayClass: *newRandomGatewayClass(),
				},
				httpRoute: makeRandomHTTPRoute(),
			}

			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			expectedErr := errors.New(faker.Sentence())

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockK8sClient.EXPECT().Status().Return(mockStatusWriter)

			mockStatusWriter.EXPECT().
				Update(t.Context(), mock.Anything).
				Return(expectedErr)

			_, err := model.acceptRoute(t.Context(), routeData)

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
		})
	})

	t.Run("resolveBackendRefs", func(t *testing.T) {
		t.Run("valid backend refs", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			backendRefs1 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}
			backendRefs2 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(randomBackendRefWithNillNamespaceOpt()),
				makeRandomBackendRef(),
			}

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRefs1...),
					),
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRefs2...),
					),
				),
			)

			allBackendRefs := make([]gatewayv1.HTTPBackendRef, 0, len(backendRefs1)+len(backendRefs2))
			allBackendRefs = append(allBackendRefs, backendRefs1...)
			allBackendRefs = append(allBackendRefs, backendRefs2...)
			services := lo.Map(allBackendRefs, func(ref gatewayv1.HTTPBackendRef, _ int) corev1.Service {
				return makeRandomService(randomServiceFromBackendRef(ref, &httpRoute))
			})

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			for _, service := range services {
				setupClientGet(t, mockK8sClient, types.NamespacedName{
					Namespace: service.Namespace,
					Name:      service.Name,
				}, service)
			}

			resolvedBackendRefs, err := model.resolveBackendRefs(t.Context(), resolveBackendRefsParams{
				httpRoute: httpRoute,
			})

			require.NoError(t, err)
			require.Len(t, resolvedBackendRefs, len(services))
			for _, service := range services {
				fullName := types.NamespacedName{
					Namespace: service.Namespace,
					Name:      service.Name,
				}
				assert.Equal(t, service, resolvedBackendRefs[fullName.String()])
			}
		})

		t.Run("backend service get error", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			backendRef := makeRandomBackendRef()
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomRulesOpt(
					makeRandomHTTPRouteRule(
						randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef),
					),
				),
			)

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			expectedErr := errors.New(faker.Sentence())
			mockK8sClient.EXPECT().Get(t.Context(), mock.Anything, mock.Anything).Return(expectedErr)

			_, err := model.resolveBackendRefs(t.Context(), resolveBackendRefsParams{
				httpRoute: httpRoute,
			})

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
		})
	})

	t.Run("programRoute", func(t *testing.T) {
		t.Run("reconcile backend set with backends", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			backendRefs1 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}
			backendRefs2 := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(randomBackendRefWithNillNamespaceOpt()),
				makeRandomBackendRef(),
			}

			rule1 := makeRandomHTTPRouteRule(
				randomHTTPRouteRuleWithRandomNameOpt(),
				randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRefs1...),
			)
			rule2 := makeRandomHTTPRouteRule(
				randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRefs2...),
			)

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomRulesOpt(
					rule1,
					rule2,
				),
			)

			wantBsNames := lo.Map(httpRoute.Spec.Rules, func(rule gatewayv1.HTTPRouteRule, i int) string {
				if rule.Name == nil {
					return fmt.Sprintf("%s-rt-%d", httpRoute.Name, i)
				}
				return fmt.Sprintf("%s-%s", httpRoute.Name, *rule.Name)
			})
			wantBss := lo.Map(wantBsNames, func(name string, _ int) loadbalancer.BackendSet {
				return makeRandomBackendSet(randomBackendSetWithNameOpt(name))
			})

			allBackendRefs := make([]gatewayv1.HTTPBackendRef, 0, len(backendRefs1)+len(backendRefs2))
			allBackendRefs = append(allBackendRefs, backendRefs1...)
			allBackendRefs = append(allBackendRefs, backendRefs2...)
			services := lo.Map(allBackendRefs, func(ref gatewayv1.HTTPBackendRef, _ int) corev1.Service {
				return makeRandomService(randomServiceFromBackendRef(ref, &httpRoute))
			})
			resolvedBackendRefs := lo.SliceToMap(services, func(svc corev1.Service) (string, corev1.Service) {
				return types.NamespacedName{
					Namespace: svc.Namespace,
					Name:      svc.Name,
				}.String(), svc
			})

			params := programRouteParams{
				gateway:             *newRandomGateway(),
				config:              makeRandomGatewayConfig(),
				httpRoute:           httpRoute,
				resolvedBackendRefs: resolvedBackendRefs,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			var nextBs loadbalancer.BackendSet
			for i, wantBs := range wantBss {
				nextBs = wantBs
				rule := httpRoute.Spec.Rules[i]
				firstBackendRef := rule.BackendRefs[0]
				port := int32(*firstBackendRef.BackendRef.Port)
				ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
					loadBalancerID: params.config.Spec.LoadBalancerID,
					name:           *wantBs.Name,
					healthChecker: &loadbalancer.HealthCheckerDetails{
						Protocol: lo.ToPtr("TCP"),
						Port:     lo.ToPtr(int(port)),
					},
				}).Return(nextBs, nil)
			}

			err := model.programRoute(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("reconcile backend set error", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			backendRef := makeRandomBackendRef()
			rule1 := makeRandomHTTPRouteRule(
				randomHTTPRouteRuleWithRandomNameOpt(),
				randomHTTPRouteRuleWithRandomBackendRefsOpt(backendRef),
			)
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomRulesOpt(rule1),
			)

			wantBsName := fmt.Sprintf("%s-%s", httpRoute.Name, *rule1.Name)
			service := makeRandomService(randomServiceFromBackendRef(backendRef, &httpRoute))
			resolvedBackendRefs := map[string]corev1.Service{
				types.NamespacedName{Namespace: service.Namespace, Name: service.Name}.String(): service,
			}

			params := programRouteParams{
				gateway:             *newRandomGateway(),
				config:              makeRandomGatewayConfig(),
				httpRoute:           httpRoute,
				resolvedBackendRefs: resolvedBackendRefs,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			expectedErr := errors.New(faker.Sentence())
			port := int32(*backendRef.BackendRef.Port)
			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
				loadBalancerID: params.config.Spec.LoadBalancerID,
				name:           wantBsName,
				healthChecker: &loadbalancer.HealthCheckerDetails{
					Protocol: lo.ToPtr("TCP"),
					Port:     lo.ToPtr(int(port)),
				},
			}).Return(loadbalancer.BackendSet{}, expectedErr)

			err := model.programRoute(t.Context(), params)
			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
		})
	})

	t.Run("isProgrammingRequired", func(t *testing.T) {
		// Helper to create base details for isProgrammingRequired tests
		newIsProgrammingRequiredDetails := func() (gatewayv1.GatewayController, resolvedRouteDetails) {
			controllerName := gatewayv1.GatewayController(faker.DomainName())
			route := makeRandomHTTPRoute()
			route.Generation = rand.Int64N(10000) + 1 // Start with a random generation
			details := resolvedRouteDetails{
				httpRoute: route,
				gatewayDetails: resolvedGatewayDetails{
					gatewayClass: *newRandomGatewayClass(randomGatewayClassWithControllerNameOpt(controllerName)),
					gateway:      *newRandomGateway(),
					config:       makeRandomGatewayConfig(),
				},
				matchedRef: gatewayv1.ParentReference{
					Namespace: lo.ToPtr(gatewayv1.Namespace(route.Namespace)),
					Name:      gatewayv1.ObjectName(faker.Word()),
				},
			}
			return controllerName, details
		}

		t.Run("ProgrammingRequired/NoMatchingParentStatus", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			_, details := newIsProgrammingRequiredDetails()

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
				{ControllerName: gatewayv1.GatewayController(faker.Word())}, // Different controller
			}

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.True(t, required, "Programming should be required when no matching parent status exists")
		})

		t.Run("ProgrammingRequired/ParentStatusFound_NoResolvedRefsCondition", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			controllerName, details := newIsProgrammingRequiredDetails()

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ControllerName: controllerName,
					ParentRef:      details.matchedRef,
					Conditions:     []metav1.Condition{}, // No ResolvedRefs condition
				},
			}

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.True(t, required, "Programming should be required when ResolvedRefs condition is missing")
		})

		t.Run("ProgrammingRequired/ResolvedRefsCondition_StatusFalse", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			controllerName, details := newIsProgrammingRequiredDetails()

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ControllerName: controllerName,
					ParentRef:      details.matchedRef,
					Conditions: []metav1.Condition{
						{
							Type:   string(gatewayv1.RouteConditionResolvedRefs),
							Status: metav1.ConditionFalse, // Status is False
						},
					},
				},
			}

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.True(t, required, "Programming should be required when ResolvedRefs status is False")
		})

		t.Run("ProgrammingRequired/ResolvedRefsCondition_GenerationMismatch", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			controllerName, details := newIsProgrammingRequiredDetails()
			observedGeneration := details.httpRoute.Generation    // Get the initial random generation
			details.httpRoute.Generation = observedGeneration + 1 // Increment current generation

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ControllerName: controllerName,
					ParentRef:      details.matchedRef,
					Conditions: []metav1.Condition{
						{
							Type:               string(gatewayv1.RouteConditionResolvedRefs),
							Status:             metav1.ConditionTrue,
							ObservedGeneration: observedGeneration, // Old generation
						},
					},
				},
			}

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.True(t, required, "Programming should be required when ObservedGeneration doesn't match")
		})

		t.Run("ProgrammingNotRequired/ResolvedRefsCondition_StatusTrue_GenerationMatch", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			controllerName, details := newIsProgrammingRequiredDetails()
			currentGeneration := details.httpRoute.Generation // Get the initial random generation

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ControllerName: controllerName,
					ParentRef:      details.matchedRef,
					Conditions: []metav1.Condition{
						{
							Type:               string(gatewayv1.RouteConditionResolvedRefs),
							Status:             metav1.ConditionTrue,
							ObservedGeneration: currentGeneration, // Matching generation
						},
					},
				},
			}

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.False(t, required, "Programming should not be required when status is True and generation matches")
		})

		t.Run("ProgrammingRequired/ParentRefMismatch", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			controllerName, details := newIsProgrammingRequiredDetails()
			currentGeneration := details.httpRoute.Generation

			mismatchedParentRef := details.matchedRef
			mismatchedParentRef.Name = gatewayv1.ObjectName(faker.Word()) // Different name

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ControllerName: controllerName,
					ParentRef:      mismatchedParentRef, // Mismatched ref
					Conditions: []metav1.Condition{
						{
							Type:               string(gatewayv1.RouteConditionResolvedRefs),
							Status:             metav1.ConditionTrue,
							ObservedGeneration: currentGeneration, // Correct condition & generation
						},
					},
				},
			}

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.True(t, required)
		})

		t.Run("ProgrammingNotRequired/CorrectParentRefFound", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			controllerName, details := newIsProgrammingRequiredDetails()
			currentGeneration := details.httpRoute.Generation

			mismatchedParentRef := details.matchedRef
			mismatchedParentRef.Name = gatewayv1.ObjectName(faker.Word())
			statusMismatchedRef := gatewayv1.RouteParentStatus{
				ControllerName: controllerName,
				ParentRef:      mismatchedParentRef,
				Conditions: []metav1.Condition{
					{
						Type:   string(gatewayv1.RouteConditionResolvedRefs),
						Status: metav1.ConditionFalse, // Different condition
					},
				},
			}

			statusCorrectRef := gatewayv1.RouteParentStatus{
				ControllerName: controllerName,
				ParentRef:      details.matchedRef, // Correct ref
				Conditions: []metav1.Condition{
					{
						Type:               string(gatewayv1.RouteConditionResolvedRefs),
						Status:             metav1.ConditionTrue,
						ObservedGeneration: currentGeneration, // Correct condition & generation
					},
				},
			}

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{statusMismatchedRef, statusCorrectRef}

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.False(t, required, "Programming should not be required when the correct ParentRef status is found and valid")
		})

		t.Run("ProgrammingRequired/MatchedParentNotReady_OtherParentReady", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			controllerName, details := newIsProgrammingRequiredDetails()
			currentGeneration := details.httpRoute.Generation

			statusMatchedRefNotReady := gatewayv1.RouteParentStatus{
				ControllerName: controllerName,
				ParentRef:      details.matchedRef, // Correct ref
				Conditions: []metav1.Condition{
					{
						Type:               string(gatewayv1.RouteConditionResolvedRefs),
						Status:             metav1.ConditionTrue,
						ObservedGeneration: currentGeneration - 1, // Mismatched generation
					},
				},
			}

			mismatchedParentRef := details.matchedRef
			mismatchedParentRef.Name = gatewayv1.ObjectName(faker.Word())
			statusOtherRefReady := gatewayv1.RouteParentStatus{
				ControllerName: controllerName,
				ParentRef:      mismatchedParentRef,
				Conditions: []metav1.Condition{
					{
						Type:               string(gatewayv1.RouteConditionResolvedRefs),
						Status:             metav1.ConditionTrue,
						ObservedGeneration: currentGeneration, // Correct condition & generation
					},
				},
			}

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{statusMatchedRefNotReady, statusOtherRefReady}

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.True(t, required)
		})
	})

	t.Run("setProgrammed", func(t *testing.T) {
		t.Run("success", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			route := makeRandomHTTPRoute()
			route.Generation = rand.Int64N(1000) + 1
			gatewayData := makeRandomAcceptedGatewayDetails()
			matchedRef := makeRandomParentRef()
			parentStatusIndex := rand.IntN(5)

			route.Status.Parents = make([]gatewayv1.RouteParentStatus, parentStatusIndex+2)
			for i := range route.Status.Parents {
				route.Status.Parents[i] = gatewayv1.RouteParentStatus{
					ParentRef:      makeRandomParentRef(),
					ControllerName: gatewayData.gatewayClass.Spec.ControllerName,
					Conditions:     []metav1.Condition{{Type: "SomeOther", Status: metav1.ConditionTrue}},
				}
			}
			// Set the correct parent ref for the target index
			route.Status.Parents[parentStatusIndex].ParentRef = matchedRef

			params := setProgrammedParams{
				httpRoute:    route, // Pass value
				gatewayClass: gatewayData.gatewayClass,
				gateway:      gatewayData.gateway,
				matchedRef:   matchedRef,
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			mockK8sClient.EXPECT().Status().Return(mockStatusWriter)

			var updatedRouteInCallback *gatewayv1.HTTPRoute
			mockStatusWriter.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.HTTPRoute")).
				RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.SubResourceUpdateOption) error {
					var ok bool
					updatedRouteInCallback, ok = obj.(*gatewayv1.HTTPRoute)
					require.True(t, ok)
					// Simulate K8s update behavior: the passed object's status is updated
					// We expect the implementation to pass a DeepCopy, so the original route in details shouldn't change here.
					return nil
				})

			// The model receives details by value, so it works on a copy of httpRoute.
			err := model.setProgrammed(t.Context(), params)
			require.NoError(t, err)
			require.NotNil(t, updatedRouteInCallback, "Update should have been called")

			// Verify the status that was sent to Update is correct
			require.Len(t, updatedRouteInCallback.Status.Parents, parentStatusIndex+2)
			updatedParentStatus := updatedRouteInCallback.Status.Parents[parentStatusIndex]
			assert.Equal(t, matchedRef, updatedParentStatus.ParentRef)
			assert.Equal(t, gatewayData.gatewayClass.Spec.ControllerName, updatedParentStatus.ControllerName)

			// Check the ResolvedRefs condition
			resolvedCond := meta.FindStatusCondition(
				updatedParentStatus.Conditions,
				string(gatewayv1.RouteConditionResolvedRefs),
			)
			require.NotNil(t, resolvedCond)
			assert.Equal(t, metav1.ConditionTrue, resolvedCond.Status)
			assert.Equal(t, string(gatewayv1.RouteReasonResolvedRefs), resolvedCond.Reason)
			assert.Equal(t, params.httpRoute.Generation, resolvedCond.ObservedGeneration)
			assert.False(t, resolvedCond.LastTransitionTime.IsZero())
			assert.Contains(t, resolvedCond.Message, "Route programmed by")

			// Check that the pre-existing condition on the target status is still there
			otherCond := meta.FindStatusCondition(updatedParentStatus.Conditions, "SomeOther")
			require.NotNil(t, otherCond)
			assert.Equal(t, metav1.ConditionTrue, otherCond.Status)

			// Check that other parent statuses were not modified unexpectedly
			for i, pStatus := range updatedRouteInCallback.Status.Parents {
				if i == parentStatusIndex {
					continue
				}
				// Compare with the original status before the call
				assert.Equal(t, params.httpRoute.Status.Parents[i], pStatus, "Parent status at index %d should not be modified", i)
			}
		})

		t.Run("parent status not found (wrong controller)", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			route := makeRandomHTTPRoute()
			gatewayData := makeRandomAcceptedGatewayDetails()
			matchedRef := makeRandomParentRef()

			// Add a status, but for a different controller
			route.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ParentRef:      matchedRef,
					ControllerName: gatewayv1.GatewayController(faker.DomainName()),
				},
			}

			details := setProgrammedParams{
				httpRoute:    route,
				gatewayClass: gatewayData.gatewayClass,
				gateway:      gatewayData.gateway,
				matchedRef:   matchedRef,
			}

			err := model.setProgrammed(t.Context(), details)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "parent status not found for controller")
		})

		t.Run("parent status not found (wrong parentRef)", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			route := makeRandomHTTPRoute()
			gatewayData := makeRandomAcceptedGatewayDetails()
			matchedRef := makeRandomParentRef()
			wrongParentRef := makeRandomParentRef()

			// Add a status with the correct controller, but wrong parentRef
			route.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ParentRef:      wrongParentRef,
					ControllerName: gatewayData.gatewayClass.Spec.ControllerName,
				},
			}

			details := setProgrammedParams{
				httpRoute:    route,
				gatewayClass: gatewayData.gatewayClass,
				gateway:      gatewayData.gateway,
				matchedRef:   matchedRef, // The ref we are looking for
			}

			err := model.setProgrammed(t.Context(), details)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "parent status not found for controller")
		})

		t.Run("update fails", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			route := makeRandomHTTPRoute()
			gatewayData := makeRandomAcceptedGatewayDetails()
			matchedRef := makeRandomParentRef()

			// Add a matching parent status entry
			route.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ParentRef:      matchedRef,
					ControllerName: gatewayData.gatewayClass.Spec.ControllerName,
				},
			}

			details := setProgrammedParams{
				httpRoute:    route,
				gatewayClass: gatewayData.gatewayClass,
				gateway:      gatewayData.gateway,
				matchedRef:   matchedRef,
			}

			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			mockK8sClient.EXPECT().Status().Return(mockStatusWriter)

			updateErr := errors.New(faker.Sentence())
			mockStatusWriter.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.HTTPRoute")).Return(updateErr)

			err := model.setProgrammed(t.Context(), details)
			require.ErrorIs(t, err, updateErr)
		})
	})
}
