package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
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
			K8sClient:      NewMockk8sClient(t),
			RootLogger:     diag.RootTestLogger(),
			GatewayModel:   NewMockgatewayModel(t),
			OciLBModel:     NewMockociLoadBalancerModel(t),
			ResourcesModel: NewMockresourcesModel(t),
		}
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
			workingRef := makeRandomParentRef(
				randomParentRefWithRandomPortOpt(),
			)

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefsOpt(otherRef1, otherRef2, workingRef),
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

			wantListeners := makeFewRandomListeners()

			gatewayData := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithNameFromParentRefOpt(workingRef),
					randomGatewayWithListenersOpt(wantListeners...),
				),
			)

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

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			require.Len(t, results, 1, "should resolve exactly one parent")

			parentKey := types.NamespacedName{
				Namespace: string(lo.FromPtr(workingRef.Namespace)),
				Name:      string(workingRef.Name),
			}
			require.Contains(t, results, parentKey)
			receiver := results[parentKey]

			assert.Equal(t, route, receiver.httpRoute)
			assert.Equal(t, *gatewayData, receiver.gatewayDetails)
			assert.Equal(t, gatewayv1.ParentReference{
				Name:      workingRef.Name,
				Namespace: workingRef.Namespace,
			}, receiver.matchedRef)
			assert.Equal(t, wantListeners, receiver.matchedListeners)
		})

		t.Run("relevant parent with section name", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: faker.Word(),
					Name:      faker.Word(),
				},
			}
			wantSectionName := gatewayv1.SectionName(faker.Word())
			workingRef := makeRandomParentRef(
				func(p *gatewayv1.ParentReference) { p.SectionName = &wantSectionName },
			)

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefOpt(workingRef),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			matchingListener := makeRandomListener(
				randomListenerWithNameOpt(wantSectionName),
			)
			otherListener1 := makeRandomListener()
			otherListener2 := makeRandomListener()
			wantListeners := []gatewayv1.Listener{matchingListener}

			gatewayData := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithNameFromParentRefOpt(workingRef),
					randomGatewayWithListenersOpt(otherListener1, matchingListener, otherListener2),
				),
			)

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

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			require.Len(t, results, 1, "should resolve exactly one parent")

			parentKey := types.NamespacedName{
				Namespace: string(lo.FromPtr(workingRef.Namespace)),
				Name:      string(workingRef.Name),
			}
			require.Contains(t, results, parentKey)
			receiver := results[parentKey]

			assert.Equal(t, route, receiver.httpRoute)
			assert.Equal(t, *gatewayData, receiver.gatewayDetails)
			assert.Equal(t, gatewayv1.ParentReference{
				Name:      workingRef.Name,
				Namespace: workingRef.Namespace,
				Group:     workingRef.Group,
				Kind:      workingRef.Kind,
			}, receiver.matchedRef)
			assert.Equal(t, wantListeners, receiver.matchedListeners)
		})

		t.Run("relevant parent with multiple sections", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: faker.Word(),
					Name:      faker.Word(),
				},
			}
			sectionName1 := gatewayv1.SectionName(faker.Word())
			sectionName2 := gatewayv1.SectionName(faker.Word())
			require.NotEqual(t, sectionName1, sectionName2) // Ensure different sections

			// Two refs pointing to the same gateway, but different sections
			workingRef1 := makeRandomParentRef(
				func(p *gatewayv1.ParentReference) { p.SectionName = &sectionName1 },
			)
			workingRef2 := makeRandomParentRef(
				func(p *gatewayv1.ParentReference) {
					p.Name = workingRef1.Name
					p.Namespace = workingRef1.Namespace
					p.SectionName = &sectionName2
				},
			)

			// A third ref pointing to a different gateway
			otherRef := makeRandomParentRef()

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefsOpt(workingRef1, workingRef2, otherRef),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			// Define listeners for the gateway
			listener1 := makeRandomListener(randomListenerWithNameOpt(sectionName1))
			listener2 := makeRandomListener(randomListenerWithNameOpt(sectionName2))
			otherListener := makeRandomListener() // This one shouldn't be matched
			allGatewayListeners := []gatewayv1.Listener{otherListener, listener1, listener2}
			wantListeners := []gatewayv1.Listener{listener1, listener2} // Only these should be in the final result

			gatewayData := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithNameFromParentRefOpt(workingRef1),
					randomGatewayWithListenersOpt(allGatewayListeners...),
				),
			)

			// Mock the gateway model response for the targeted gateway
			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(workingRef1.Namespace)),
						Name:      string(workingRef1.Name),
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
			}).Times(2) // Expect it to be called twice for the two refs

			// Mock the gateway model response for the other gateway (should resolve false)
			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(otherRef.Namespace)),
						Name:      string(otherRef.Name),
					},
				},
				mock.Anything,
			).Return(false, nil)

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			require.Len(t, results, 1, "should resolve exactly one parent gateway")

			parentKey := types.NamespacedName{
				Namespace: string(lo.FromPtr(workingRef1.Namespace)),
				Name:      string(workingRef1.Name),
			}
			require.Contains(t, results, parentKey)
			receiver := results[parentKey]

			assert.Equal(t, route, receiver.httpRoute)
			assert.Equal(t, *gatewayData, receiver.gatewayDetails)
			assert.Equal(t, gatewayv1.ParentReference{
				Name:      workingRef1.Name,
				Namespace: workingRef1.Namespace,
				Group:     workingRef1.Group,
				Kind:      workingRef1.Kind,
			}, receiver.matchedRef)
			assert.ElementsMatch(t, wantListeners, receiver.matchedListeners)
		})

		t.Run("relevant parent with non-matching section name", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: faker.Word(),
					Name:      faker.Word(),
				},
			}
			nonMatchingSectionName := gatewayv1.SectionName(faker.Word())
			refWithNonMatchingSection := makeRandomParentRef(
				func(p *gatewayv1.ParentReference) { p.SectionName = &nonMatchingSectionName },
			)
			refWithoutSection := makeRandomParentRef()

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefsOpt(refWithNonMatchingSection, refWithoutSection),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			listener1 := makeRandomListener()
			gatewayData1 := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithNameFromParentRefOpt(refWithNonMatchingSection),
					randomGatewayWithListenersOpt(listener1),
				),
			)
			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(refWithNonMatchingSection.Namespace)),
						Name:      string(refWithNonMatchingSection.Name),
					},
				},
				mock.Anything,
			).RunAndReturn(func(
				_ context.Context,
				_ reconcile.Request,
				receiver *resolvedGatewayDetails,
			) (bool, error) {
				*receiver = *gatewayData1
				return true, nil
			})

			allListeners := makeFewRandomListeners()
			gatewayData2 := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithNameFromParentRefOpt(refWithoutSection),
					randomGatewayWithListenersOpt(allListeners...),
				),
			)
			gatewayModel.EXPECT().resolveReconcileRequest(
				t.Context(),
				reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: string(lo.FromPtr(refWithoutSection.Namespace)),
						Name:      string(refWithoutSection.Name),
					},
				},
				mock.Anything,
			).RunAndReturn(func(
				_ context.Context,
				_ reconcile.Request,
				receiver *resolvedGatewayDetails,
			) (bool, error) {
				*receiver = *gatewayData2
				return true, nil
			})

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			require.Len(t, results, 1, "at least one parent should resolve")

			parentKey := types.NamespacedName{
				Namespace: string(lo.FromPtr(refWithoutSection.Namespace)),
				Name:      string(refWithoutSection.Name),
			}
			require.Contains(t, results, parentKey)
			res := results[parentKey]

			assert.Equal(t, *gatewayData2, res.gatewayDetails)
			assert.Equal(t, gatewayv1.ParentReference{
				Name:      refWithoutSection.Name,
				Namespace: refWithoutSection.Namespace,
				Group:     refWithoutSection.Group,
				Kind:      refWithoutSection.Kind,
			}, res.matchedRef)
			assert.Equal(t, allListeners, res.matchedListeners)
		})

		t.Run("no relevant parent when section name does not match", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: faker.Word(),
					Name:      faker.Word(),
				},
			}
			nonMatchingSectionName := gatewayv1.SectionName(faker.Word())
			workingRef := makeRandomParentRef(
				func(p *gatewayv1.ParentReference) { p.SectionName = &nonMatchingSectionName },
			)

			route := makeRandomHTTPRoute(
				randomHTTPRouteWithRandomParentRefOpt(workingRef),
			)

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			listener1 := makeRandomListener()
			gatewayData := makeRandomAcceptedGatewayDetails(
				randomResolvedGatewayDetailsWithGatewayOpts(
					randomGatewayWithListenersOpt(listener1),
				),
			)

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

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			assert.Empty(t, results, "parent should not resolve when section name does not match any listener")
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

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			assert.Empty(t, results, "parent should not be resolved")
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

			results, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			assert.Empty(t, results, "parent should not be resolved")
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

			results, err := model.resolveRequest(t.Context(), req)

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
			assert.Nil(t, results, "should return nil results on error")
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

			results, err := model.resolveRequest(t.Context(), req)

			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr)
			assert.Nil(t, results, "should return nil results on error")
		})
	})

	t.Run("acceptRoute", func(t *testing.T) {
		t.Run("add new accepted parent", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			routeData := resolvedRouteDetails{
				matchedRef: makeRandomParentRef(
					randomParentRefWithRandomSectionNameOpt(),
					randomParentRefWithRandomPortOpt(),
				),
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
			assert.Equal(t, gatewayv1.ParentReference{
				Name:      routeData.matchedRef.Name,
				Namespace: routeData.matchedRef.Namespace,
			}, acceptedParent.ParentRef)
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
					s.ParentRef = makeRandomParentRef(
						randomParentRefWithRandomSectionNameOpt(),
						randomParentRefWithRandomPortOpt(),
					)
					s.ControllerName = gatewayClass.Spec.ControllerName
				},
			)
			routeData := resolvedRouteDetails{
				matchedRef: existingParentStatus.ParentRef,
				gatewayDetails: resolvedGatewayDetails{
					gateway:      *newRandomGateway(),
					gatewayClass: *gatewayClass,
				},
				httpRoute: makeRandomHTTPRoute(
					func(h *gatewayv1.HTTPRoute) {
						h.Status.Parents = []gatewayv1.RouteParentStatus{
							makeRandomRouteParentStatus(func(rps *gatewayv1.RouteParentStatus) {
								rps.ControllerName = gatewayClass.Spec.ControllerName
							}),
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
				return s.ControllerName == gatewayClass.Spec.ControllerName &&
					parentRefSameTarget(s.ParentRef, routeData.matchedRef)
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
				matchedRef: existingParentStatus.ParentRef,
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
				matchedRef: existingParentStatus.ParentRef,
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
				randomHTTPRouteWithRulesOpt(
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
				randomHTTPRouteWithRulesOpt(
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
		t.Run("successfully programs route with multiple listeners", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			// Setup test data
			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()

			// Create HTTP route with multiple rules
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(),
					makeRandomHTTPRouteRule(),
				),
			)

			knownServices := makeFewRandomServices()
			knownServicesByName := lo.SliceToMap(knownServices, func(s corev1.Service) (string, corev1.Service) {
				return types.NamespacedName{
					Namespace: s.Namespace,
					Name:      s.Name,
				}.String(), s
			})

			listeners := makeFewRandomListeners()

			params := programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    knownServicesByName,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			// Expect reconciliation of backend sets for each service
			for _, service := range knownServicesByName {
				ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
					loadBalancerID: config.Spec.LoadBalancerID,
					service:        service,
				}).Return(nil)
			}

			// Create expected routing rules for each HTTP route rule
			expectedRules := make([]loadbalancer.RoutingRule, 0, len(httpRoute.Spec.Rules))
			for i := range httpRoute.Spec.Rules {
				rule := makeRandomOCIRoutingRule()
				expectedRules = append(expectedRules, rule)

				ociLBModel.EXPECT().makeRoutingRule(t.Context(), makeRoutingRuleParams{
					httpRoute:          httpRoute,
					httpRouteRuleIndex: i,
				}).Return(rule, nil)
			}

			// Expect commitRoutingPolicyV2 to be called for each listener
			for _, listener := range listeners {
				ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
					loadBalancerID: config.Spec.LoadBalancerID,
					listenerName:   string(listener.Name),
					policyRules:    expectedRules,
				}).Return(nil)
			}

			_, err := model.programRoute(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("program with previously programmed annotations", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			// Setup test data
			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()

			// Create HTTP route with multiple rules
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(),
					makeRandomHTTPRouteRule(),
				),
			)
			wantPreviousRules := []string{
				"rule1-" + faker.Word(),
				"rule2-" + faker.Word(),
				"rule3-" + faker.Word(),
			}
			httpRoute.Annotations = map[string]string{
				HTTPRouteProgrammedPolicyRulesAnnotation: strings.Join(wantPreviousRules, ","),
			}

			knownServices := makeFewRandomServices()
			knownServicesByName := lo.SliceToMap(knownServices, func(s corev1.Service) (string, corev1.Service) {
				return types.NamespacedName{
					Namespace: s.Namespace,
					Name:      s.Name,
				}.String(), s
			})

			listeners := makeFewRandomListeners()

			params := programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    knownServicesByName,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			// Expect reconciliation of backend sets for each service
			for _, service := range knownServicesByName {
				ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
					loadBalancerID: config.Spec.LoadBalancerID,
					service:        service,
				}).Return(nil)
			}

			// Create expected routing rules for each HTTP route rule
			expectedRules := make([]loadbalancer.RoutingRule, 0, len(httpRoute.Spec.Rules))
			for i := range httpRoute.Spec.Rules {
				rule := makeRandomOCIRoutingRule()
				expectedRules = append(expectedRules, rule)

				ociLBModel.EXPECT().makeRoutingRule(t.Context(), makeRoutingRuleParams{
					httpRoute:          httpRoute,
					httpRouteRuleIndex: i,
				}).Return(rule, nil).Once()
			}

			// Expect commitRoutingPolicyV2 to be called for each listener
			for _, listener := range listeners {
				ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
					loadBalancerID:  config.Spec.LoadBalancerID,
					listenerName:    string(listener.Name),
					policyRules:     expectedRules,
					prevPolicyRules: wantPreviousRules,
				}).Return(nil)
			}

			_, err := model.programRoute(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("fails when reconcile backend set fails", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			// Setup test data
			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()

			httpRoute := makeRandomHTTPRoute()
			services := map[string]corev1.Service{
				"service1": makeRandomService(),
			}
			listeners := []gatewayv1.Listener{
				makeRandomListener(),
			}

			params := programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    services,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			wantErr := errors.New(faker.Sentence())

			// First service reconciliation fails
			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), mock.Anything).Return(wantErr)

			_, err := model.programRoute(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fails when makeRoutingRule fails", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			// Setup test data
			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(),
				),
			)
			services := map[string]corev1.Service{
				"service1": makeRandomService(),
			}
			listeners := []gatewayv1.Listener{
				makeRandomListener(),
			}

			params := programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    services,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			// Backend set reconciliation succeeds
			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), mock.Anything).Return(nil)

			wantErr := errors.New(faker.Sentence())

			// Making routing rule fails
			ociLBModel.EXPECT().makeRoutingRule(t.Context(), mock.Anything).Return(loadbalancer.RoutingRule{}, wantErr)

			_, err := model.programRoute(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})

		t.Run("fails when commitRoutingPolicyV2 fails", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			// Setup test data
			gateway := newRandomGateway()
			config := makeRandomGatewayConfig()

			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(
					makeRandomHTTPRouteRule(),
				),
			)
			services := map[string]corev1.Service{
				"service1": makeRandomService(),
			}
			listeners := []gatewayv1.Listener{
				makeRandomListener(),
			}

			params := programRouteParams{
				gateway:          *gateway,
				config:           config,
				httpRoute:        httpRoute,
				knownBackends:    services,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			// Backend set reconciliation succeeds
			ociLBModel.EXPECT().reconcileBackendSet(t.Context(), mock.Anything).Return(nil)

			// Making routing rule succeeds
			rule := makeRandomOCIRoutingRule()
			ociLBModel.EXPECT().makeRoutingRule(t.Context(), mock.Anything).Return(rule, nil)

			wantErr := errors.New(faker.Sentence())

			// Committing policy fails
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), mock.Anything).Return(wantErr)

			_, err := model.programRoute(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
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

		t.Run("ProgrammingRequired/ParentStatusFound", func(t *testing.T) {
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
							Status: metav1.ConditionTrue,
						},
					},
				},
			}

			checkResult := lo.Ternary(rand.IntN(2) == 0, true, false)
			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockResourcesModel.EXPECT().isConditionSet(isConditionSetParams{
				resource:      &details.httpRoute,
				conditions:    details.httpRoute.Status.Parents[0].Conditions,
				conditionType: string(gatewayv1.RouteConditionResolvedRefs),
				annotations: map[string]string{
					HTTPRouteProgrammingRevisionAnnotation: HTTPRouteProgrammingRevisionValue,
				},
			}).Return(checkResult)

			required, err := model.isProgrammingRequired(details)
			require.NoError(t, err)
			assert.Equal(t, !checkResult, required)
		})

		t.Run("ProgrammingRequired/ParentRefMismatch", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)
			controllerName, details := newIsProgrammingRequiredDetails()

			mismatchedParentRef := details.matchedRef
			mismatchedParentRef.Name = gatewayv1.ObjectName(faker.Word()) // Different name

			details.httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
				{
					ControllerName: controllerName,
					ParentRef:      mismatchedParentRef, // Mismatched ref
				},
			}

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
				httpRoute:    route,
				gatewayClass: gatewayData.gatewayClass,
				gateway:      gatewayData.gateway,
				matchedRef:   matchedRef,
				programmedPolicyRules: []string{
					"rule1-" + faker.Word(),
					"rule2-" + faker.Word(),
					"rule3-" + faker.Word(),
				},
			}

			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockResourcesModel.EXPECT().setCondition(t.Context(), setConditionParams{
				resource:      &route,
				conditions:    &route.Status.Parents[parentStatusIndex].Conditions,
				conditionType: string(gatewayv1.RouteConditionResolvedRefs),
				status:        metav1.ConditionTrue,
				reason:        string(gatewayv1.RouteReasonResolvedRefs),
				message:       fmt.Sprintf("Route programmed by %s", params.gateway.Name),
				annotations: map[string]string{
					HTTPRouteProgrammingRevisionAnnotation:   HTTPRouteProgrammingRevisionValue,
					HTTPRouteProgrammedPolicyRulesAnnotation: strings.Join(params.programmedPolicyRules, ","),
				},
				finalizer: HTTPRouteProgrammedFinalizer,
			}).Return(nil)

			// The model receives details by value, so it works on a copy of httpRoute.
			err := model.setProgrammed(t.Context(), params)
			require.NoError(t, err)
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

			updateErr := errors.New(faker.Sentence())
			mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			mockResourcesModel.EXPECT().setCondition(t.Context(), mock.Anything).Return(updateErr)

			err := model.setProgrammed(t.Context(), details)
			require.ErrorIs(t, err, updateErr)
		})
	})

	t.Run("deprovisionRoute", func(t *testing.T) {
		t.Run("successfully deprovisions route with multiple listeners", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			wantBackendRefs := []gatewayv1.HTTPBackendRef{
				makeRandomBackendRef(),
				makeRandomBackendRef(),
				makeRandomBackendRef(),
			}

			backendResRules := lo.Map(wantBackendRefs, func(br gatewayv1.HTTPBackendRef, _ int) gatewayv1.HTTPRouteRule {
				return makeRandomHTTPRouteRule(randomHTTPRouteRuleWithRandomBackendRefsOpt(br))
			})

			config := makeRandomGatewayConfig()
			httpRoute := makeRandomHTTPRoute(
				randomHTTPRouteWithRulesOpt(backendResRules...),
			)

			wantPreviousRules := []string{
				"rule1-" + faker.Word(),
				"rule2-" + faker.Word(),
			}
			annotationValue := strings.Join(wantPreviousRules, ",")
			httpRoute.Annotations = map[string]string{
				HTTPRouteProgrammedPolicyRulesAnnotation: annotationValue,
			}
			httpRoute.Finalizers = []string{
				HTTPRouteProgrammedFinalizer,
				faker.DomainName(),
			}

			listeners := makeFewRandomListeners()

			params := deprovisionRouteParams{
				gateway:          *newRandomGateway(),
				config:           config,
				httpRoute:        httpRoute,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)

			var lastCommitCall *mock.Call
			for _, listener := range listeners {
				lastCommitCall = ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
					loadBalancerID:  config.Spec.LoadBalancerID,
					listenerName:    string(listener.Name),
					policyRules:     []loadbalancer.RoutingRule{}, // Important: No rules to program
					prevPolicyRules: wantPreviousRules,
				}).Return(nil).Once()
			}

			for _, backendRef := range wantBackendRefs {
				ociLBModel.EXPECT().deprovisionBackendSet(t.Context(), deprovisionBackendSetParams{
					loadBalancerID: config.Spec.LoadBalancerID,
					httpRoute:      httpRoute,
					backendRef:     backendRef,
				}).Return(nil).Once().NotBefore(lastCommitCall)
			}

			// Expect client update for finalizer removal
			mockK8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			var updatedRoute *gatewayv1.HTTPRoute
			mockK8sClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				var ok bool
				updatedRoute, ok = obj.(*gatewayv1.HTTPRoute)

				assert.NotContains(t, updatedRoute.Finalizers, HTTPRouteProgrammedFinalizer)

				return ok && assert.Equal(t, httpRoute.Name, updatedRoute.Name)
			})).Return(nil)

			err := model.deprovisionRoute(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("successfully deprovisions route with no previous rules annotation", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			config := makeRandomGatewayConfig()
			httpRoute := makeRandomHTTPRoute()
			listeners := makeFewRandomListeners()

			params := deprovisionRouteParams{
				gateway:          *newRandomGateway(),
				config:           config,
				httpRoute:        httpRoute,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			// Expect commitRoutingPolicy NOT to be called
			ociLBModel.AssertNotCalled(t, "commitRoutingPolicy", mock.Anything, mock.Anything)

			err := model.deprovisionRoute(t.Context(), params)
			require.NoError(t, err)
		})

		t.Run("fails when commitRoutingPolicy fails", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			config := makeRandomGatewayConfig()
			httpRoute := makeRandomHTTPRoute()
			// Ensure prevPolicyRules annotation is present so the method doesn't return early
			httpRoute.Annotations = map[string]string{
				HTTPRouteProgrammedPolicyRulesAnnotation: "rule1,rule2",
			}
			listeners := []gatewayv1.Listener{makeRandomListener()} // Just one for simplicity

			params := deprovisionRouteParams{
				gateway:          *newRandomGateway(),
				config:           config,
				httpRoute:        httpRoute,
				matchedListeners: listeners,
			}

			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			wantErr := errors.New(faker.Sentence())

			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), mock.Anything).Return(wantErr)

			err := model.deprovisionRoute(t.Context(), params)
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErr)
		})
	})
}
