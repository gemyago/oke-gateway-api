package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
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
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
)

func TestGRPCRouteModelImpl(t *testing.T) {
	newMockDeps := func(t *testing.T) grpcRouteModelDeps {
		return grpcRouteModelDeps{
			K8sClient:      NewMockk8sClient(t),
			RootLogger:     diag.RootTestLogger(),
			GatewayModel:   NewMockgatewayModel(t),
			OciLBModel:     NewMockociLoadBalancerModel(t),
			ResourcesModel: NewMockresourcesModel(t),
		}
	}
	makeGRPCRoute := func(opts ...func(*gatewayv1.GRPCRoute)) gatewayv1.GRPCRoute {
		fake := faker.New()
		route := gatewayv1.GRPCRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "ns-" + fake.Lorem().Word(),
				Name:      "grpc-" + fake.Lorem().Word(),
			},
			Spec: gatewayv1.GRPCRouteSpec{},
		}
		for _, opt := range opts {
			opt(&route)
		}
		return route
	}
	makeGRPCBackendRef := func(opts ...func(*gatewayv1.GRPCBackendRef)) gatewayv1.GRPCBackendRef {
		fake := faker.New()
		backendRef := gatewayv1.GRPCBackendRef{
			BackendRef: gatewayv1.BackendRef{
				BackendObjectReference: gatewayv1.BackendObjectReference{
					Name: gatewayv1.ObjectName("svc-" + fake.Lorem().Word()),
					Port: lo.ToPtr(gatewayv1.PortNumber(50051)),
				},
			},
		}
		for _, opt := range opts {
			opt(&backendRef)
		}
		return backendRef
	}
	makeResolvedGateway := func(listeners ...gatewayv1.Listener) resolvedGatewayDetails {
		fake := faker.New()
		if len(listeners) == 0 {
			listeners = []gatewayv1.Listener{
				{Name: "grpc", Port: 50051, Protocol: gatewayv1.HTTPSProtocolType},
				{Name: "https", Port: 443, Protocol: gatewayv1.HTTPSProtocolType},
			}
		}
		for i := range listeners {
			if listeners[i].Protocol == "" {
				listeners[i].Protocol = gatewayv1.HTTPSProtocolType
			}
		}
		return resolvedGatewayDetails{
			gateway: gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "gw-ns-" + fake.Lorem().Word(),
					Name:      "gw-" + fake.Lorem().Word(),
				},
				Spec: gatewayv1.GatewaySpec{Listeners: listeners},
			},
			gatewayClass: gatewayv1.GatewayClass{
				Spec: gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
			},
			config: makeRandomGatewayConfig(),
		}
	}

	t.Run("resolveRouteParentRefData", func(t *testing.T) {
		t.Run("returns all listeners when section is not set", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)
			route := makeGRPCRoute()
			parentRef := gatewayv1.ParentReference{Name: "gw"}
			gatewayData := makeResolvedGateway()
			gatewayData.gateway.Namespace = route.Namespace
			gatewayData.gateway.Name = string(parentRef.Name)

			gatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), mock.Anything, mock.Anything).
				RunAndReturn(func(_ context.Context, _ reconcile.Request, receiver *resolvedGatewayDetails) (bool, error) {
					*receiver = gatewayData
					return true, nil
				})

			gotGatewayData, gotListeners, err := model.resolveRouteParentRefData(
				t.Context(),
				route,
				parentRef,
				route.Namespace,
			)

			require.NoError(t, err)
			require.NotNil(t, gotGatewayData)
			assert.Equal(t, gatewayData.gateway.Name, gotGatewayData.gateway.Name)
			assert.Equal(t, gatewayData.gateway.Spec.Listeners, gotListeners)
		})

		t.Run("filters listeners by section name", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)
			route := makeGRPCRoute()
			sectionName := gatewayv1.SectionName("grpc")
			parentRef := gatewayv1.ParentReference{Name: "gw", SectionName: &sectionName}
			grpcListener := gatewayv1.Listener{Name: sectionName, Port: 50051, Protocol: gatewayv1.HTTPSProtocolType}
			gatewayData := makeResolvedGateway(grpcListener, gatewayv1.Listener{Name: "web", Port: 443})
			gatewayData.gateway.Namespace = route.Namespace
			gatewayData.gateway.Name = string(parentRef.Name)

			gatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), mock.Anything, mock.Anything).
				RunAndReturn(func(_ context.Context, _ reconcile.Request, receiver *resolvedGatewayDetails) (bool, error) {
					*receiver = gatewayData
					return true, nil
				})

			_, gotListeners, err := model.resolveRouteParentRefData(t.Context(), route, parentRef, route.Namespace)

			require.NoError(t, err)
			assert.Equal(t, []gatewayv1.Listener{grpcListener}, gotListeners)
		})

		t.Run("resolves ListenerSet parent refs by logical section name", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)
			route := makeGRPCRoute()
			route.Namespace = "apps"
			sectionName := gatewayv1.SectionName("grpc")
			listenerSetKind := gatewayv1.Kind("ListenerSet")
			parentNamespace := gatewayv1.Namespace("infra")
			parentRef := gatewayv1.ParentReference{
				Kind:        &listenerSetKind,
				Name:        "extra",
				SectionName: &sectionName,
			}
			listenerSet := gatewayv1.ListenerSet{
				ObjectMeta: metav1.ObjectMeta{Namespace: route.Namespace, Name: string(parentRef.Name)},
				Spec: gatewayv1.ListenerSetSpec{
					ParentRef: gatewayv1.ParentGatewayReference{
						Namespace: &parentNamespace,
						Name:      "edge",
					},
					Listeners: []gatewayv1.ListenerEntry{{
						Name:     sectionName,
						Port:     443,
						Protocol: gatewayv1.HTTPSProtocolType,
					}},
				},
			}
			fromAll := gatewayv1.NamespacesFromAll
			gatewayData := makeResolvedGateway()
			gatewayData.gateway.Namespace = string(parentNamespace)
			gatewayData.gateway.Name = "edge"
			gatewayData.gateway.Spec.Listeners = nil
			gatewayData.gateway.Spec.AllowedListeners = &gatewayv1.AllowedListeners{
				Namespaces: &gatewayv1.ListenerNamespaces{From: &fromAll},
			}

			setupClientGet(t, deps.K8sClient, apitypes.NamespacedName{
				Namespace: listenerSet.Namespace,
				Name:      listenerSet.Name,
			}, listenerSet)
			gatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), reconcile.Request{
					NamespacedName: apitypes.NamespacedName{Namespace: "infra", Name: "edge"},
				}, mock.Anything).
				RunAndReturn(func(_ context.Context, _ reconcile.Request, receiver *resolvedGatewayDetails) (bool, error) {
					*receiver = gatewayData
					return true, nil
				})
			mockClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockClient.EXPECT().
				List(t.Context(), &gatewayv1.ListenerSetList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					reflect.ValueOf(list).
						Elem().
						FieldByName("Items").
						Set(reflect.ValueOf([]gatewayv1.ListenerSet{listenerSet}))
					return nil
				})
			setupClientGet(t, mockClient, apitypes.NamespacedName{Name: listenerSet.Namespace}, corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: listenerSet.Namespace},
			})

			gotGatewayData, gotListeners, err := model.resolveRouteParentRefData(
				t.Context(),
				route,
				parentRef,
				route.Namespace,
			)

			require.NoError(t, err)
			require.NotNil(t, gotGatewayData)
			require.Len(t, gotListeners, 1)
			assert.NotEqual(t, sectionName, gotListeners[0].Name)
			assert.Equal(t, gatewayv1.HTTPSProtocolType, gotListeners[0].Protocol)
		})

		t.Run("returns nil when section listener protocol is unsupported", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)
			route := makeGRPCRoute()
			sectionName := gatewayv1.SectionName("grpc")
			parentRef := gatewayv1.ParentReference{Name: "gw", SectionName: &sectionName}
			gatewayData := makeResolvedGateway(gatewayv1.Listener{
				Name:     sectionName,
				Port:     50051,
				Protocol: gatewayv1.TCPProtocolType,
			})
			gatewayData.gateway.Namespace = route.Namespace
			gatewayData.gateway.Name = string(parentRef.Name)

			gatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), mock.Anything, mock.Anything).
				RunAndReturn(func(_ context.Context, _ reconcile.Request, receiver *resolvedGatewayDetails) (bool, error) {
					*receiver = gatewayData
					return true, nil
				})

			gotGatewayData, gotListeners, err := model.resolveRouteParentRefData(
				t.Context(),
				route,
				parentRef,
				route.Namespace,
			)

			require.NoError(t, err)
			assert.Nil(t, gotGatewayData)
			assert.Nil(t, gotListeners)
		})

		t.Run("uses parent ref namespace when provided", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)
			route := makeGRPCRoute()
			parentNamespace := gatewayv1.Namespace("parent-ns")
			parentRef := gatewayv1.ParentReference{Name: "gw", Namespace: &parentNamespace}

			gatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), mock.Anything, mock.Anything).
				RunAndReturn(func(_ context.Context, req reconcile.Request, _ *resolvedGatewayDetails) (bool, error) {
					assert.Equal(t, string(parentNamespace), req.Namespace)
					return false, nil
				})

			gotGatewayData, gotListeners, err := model.resolveRouteParentRefData(
				t.Context(),
				route,
				parentRef,
				route.Namespace,
			)

			require.NoError(t, err)
			assert.Nil(t, gotGatewayData)
			assert.Nil(t, gotListeners)
		})

		t.Run("returns nil when gateway does not resolve", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

			gatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), mock.Anything, mock.Anything).
				Return(false, nil)

			gotGatewayData, gotListeners, err := model.resolveRouteParentRefData(
				t.Context(),
				makeGRPCRoute(),
				gatewayv1.ParentReference{Name: "gw"},
				"default",
			)

			require.NoError(t, err)
			assert.Nil(t, gotGatewayData)
			assert.Nil(t, gotListeners)
		})

		t.Run("wraps gateway resolution errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))

			gatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), mock.Anything, mock.Anything).
				Return(false, wantErr)

			_, _, err := model.resolveRouteParentRefData(
				t.Context(),
				makeGRPCRoute(),
				gatewayv1.ParentReference{Name: "gw"},
				"default",
			)

			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("resolveRequest", func(t *testing.T) {
		t.Run("cleans deleting programmed route with no resolved parent", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			loadBalancerID := "lb-" + fake.UUID().V4()
			listenerName := "listener-" + fake.Lorem().Word()
			ruleName := "rule-" + fake.Lorem().Word()
			deleteTime := metav1.Now()
			backendRef := makeGRPCBackendRef()
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Spec.Rules = []gatewayv1.GRPCRouteRule{{BackendRefs: []gatewayv1.GRPCBackendRef{backendRef}}}
				route.DeletionTimestamp = &deleteTime
				route.Finalizers = []string{GRPCRouteProgrammedFinalizer}
				route.Annotations = map[string]string{
					GRPCRouteProgrammedPolicyRulesAnnotation:  listenerName + "/" + ruleName,
					L7RouteProgrammedLoadBalancerIDAnnotation: loadBalancerID,
				}
			})
			req := reconcile.Request{
				NamespacedName: apitypes.NamespacedName{Namespace: route.Namespace, Name: route.Name},
			}

			k8sClient.EXPECT().Get(t.Context(), req.NamespacedName, mock.AnythingOfType("*v1.GRPCRoute")).
				Run(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) {
					*obj.(*gatewayv1.GRPCRoute) = route
				}).
				Return(nil).
				Once()
			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  loadBalancerID,
				listenerName:    listenerName,
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{ruleName},
			}).Return(nil).Once()
			ociLBModel.EXPECT().deprovisionBackendSet(t.Context(), deprovisionBackendSetParams{
				loadBalancerID: loadBalancerID,
				routeNamespace: route.Namespace,
				backendRef:     backendRef.BackendRef,
			}).Return(nil).Once()
			k8sClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				updated, ok := obj.(*gatewayv1.GRPCRoute)
				return ok &&
					!controllerutil.ContainsFinalizer(updated, GRPCRouteProgrammedFinalizer) &&
					updated.Annotations[GRPCRouteProgrammedPolicyRulesAnnotation] == "" &&
					updated.Annotations[L7RouteProgrammedLoadBalancerIDAnnotation] == ""
			})).Return(nil).Once()

			got, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			assert.Empty(t, got)
		})

		t.Run("wraps detached finalizer update errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			deleteTime := metav1.Now()
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.DeletionTimestamp = &deleteTime
				route.Finalizers = []string{GRPCRouteProgrammedFinalizer}
			})
			req := reconcile.Request{
				NamespacedName: apitypes.NamespacedName{Namespace: route.Namespace, Name: route.Name},
			}
			wantErr := errors.New("update failed")

			k8sClient.EXPECT().Get(t.Context(), req.NamespacedName, mock.AnythingOfType("*v1.GRPCRoute")).
				Run(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) {
					*obj.(*gatewayv1.GRPCRoute) = route
				}).
				Return(nil).
				Once()
			k8sClient.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.GRPCRoute")).
				Return(wantErr).
				Once()

			_, err := model.resolveRequest(t.Context(), req)

			require.ErrorIs(t, err, wantErr)
			require.ErrorContains(t, err, "failed to deprovision detached GRPCRoute")
		})

		t.Run("returns empty result when route is not found", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			req := reconcile.Request{NamespacedName: apitypes.NamespacedName{Namespace: "default", Name: "missing"}}
			k8sClient.EXPECT().Get(t.Context(), req.NamespacedName, mock.Anything).Return(
				apierrors.NewNotFound(schema.GroupResource{Group: gatewayv1.GroupName, Resource: "grpcroutes"}, req.Name),
			)

			got, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			assert.Empty(t, got)
		})

		t.Run("returns route get errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			req := reconcile.Request{NamespacedName: apitypes.NamespacedName{
				Namespace: "ns-" + faker.New().Lorem().Word(),
				Name:      "grpc-" + faker.New().Lorem().Word(),
			}}
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			k8sClient.EXPECT().Get(t.Context(), req.NamespacedName, mock.Anything).Return(wantErr)

			_, err := model.resolveRequest(t.Context(), req)

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("aggregates listeners for the same gateway", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)
			grpcSection := gatewayv1.SectionName("grpc")
			httpsSection := gatewayv1.SectionName("https")
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Spec.ParentRefs = []gatewayv1.ParentReference{
					{Name: "gw", SectionName: &grpcSection},
					{Name: "gw", SectionName: &httpsSection},
				}
			})
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)}
			gatewayData := makeResolvedGateway(
				gatewayv1.Listener{Name: grpcSection, Port: 50051},
				gatewayv1.Listener{Name: httpsSection, Port: 443},
			)
			gatewayData.gateway.Namespace = route.Namespace
			gatewayData.gateway.Name = "gw"

			k8sClient.EXPECT().Get(t.Context(), req.NamespacedName, mock.Anything).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.GRPCRoute) = route
					return nil
				})
			gatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), mock.Anything, mock.Anything).
				RunAndReturn(func(
					_ context.Context,
					_ reconcile.Request,
					receiver *resolvedGatewayDetails,
				) (bool, error) {
					*receiver = gatewayData
					return true, nil
				}).Twice()

			got, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			result := got[client.ObjectKeyFromObject(&gatewayData.gateway)]
			assert.Len(t, result.matchedListeners, 2)
		})

		t.Run("skips parent refs when gateway does not resolve", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			gatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Spec.ParentRefs = []gatewayv1.ParentReference{{Name: "missing-gateway"}}
			})
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)}

			k8sClient.EXPECT().Get(t.Context(), req.NamespacedName, mock.Anything).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*gatewayv1.GRPCRoute) = route
					return nil
				})
			gatewayModel.EXPECT().
				resolveReconcileRequest(t.Context(), mock.Anything, mock.Anything).
				Return(false, nil).
				Once()

			got, err := model.resolveRequest(t.Context(), req)

			require.NoError(t, err)
			assert.Empty(t, got)
		})
	})

	t.Run("acceptRoute", func(t *testing.T) {
		t.Run("adds accepted parent status", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			route := makeGRPCRoute()
			gatewayData := makeResolvedGateway()
			parentRef := gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gatewayData.gateway.Name)}

			k8sClient.EXPECT().Status().Return(mockStatusWriter)
			var updatedRoute *gatewayv1.GRPCRoute
			mockStatusWriter.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				var ok bool
				updatedRoute, ok = obj.(*gatewayv1.GRPCRoute)
				return ok
			})).Return(nil)

			got, err := model.acceptRoute(t.Context(), resolvedGRPCRouteDetails{
				gatewayDetails: gatewayData,
				grpcRoute:      route,
				matchedRef:     parentRef,
			})

			require.NoError(t, err)
			assert.Same(t, updatedRoute, got)
			require.Len(t, updatedRoute.Status.Parents, 1)
			gotCondition := meta.FindStatusCondition(
				updatedRoute.Status.Parents[0].Conditions,
				string(gatewayv1.RouteConditionAccepted),
			)
			require.NotNil(t, gotCondition)
			assert.Equal(t, metav1.ConditionTrue, gotCondition.Status)
		})

		t.Run("accepts when an older HTTPRoute shares the same Gateway listener and hostname", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			listenerName := gatewayv1.SectionName("https")
			hostname := gatewayv1.Hostname("grpc.example.com")
			gatewayData := makeResolvedGateway(gatewayv1.Listener{
				Name:     listenerName,
				Hostname: &hostname,
				Port:     443,
				Protocol: gatewayv1.HTTPSProtocolType,
			})
			gatewayNamespace := gatewayv1.Namespace(gatewayData.gateway.Namespace)
			parentRef := gatewayv1.ParentReference{
				Namespace:   &gatewayNamespace,
				Name:        gatewayv1.ObjectName(gatewayData.gateway.Name),
				SectionName: &listenerName,
			}
			currentRoute := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Namespace = gatewayData.gateway.Namespace
				route.CreationTimestamp = metav1.NewTime(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
				route.Spec.ParentRefs = []gatewayv1.ParentReference{parentRef}
				route.Spec.Hostnames = []gatewayv1.Hostname{hostname}
				route.Spec.Rules = []gatewayv1.GRPCRouteRule{{
					BackendRefs: []gatewayv1.GRPCBackendRef{{
						BackendRef: gatewayv1.BackendRef{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Name: gatewayv1.ObjectName("ripster"),
								Port: lo.ToPtr(gatewayv1.PortNumber(6553)),
							},
						},
					}},
				}}
			})
			olderHTTPRoute := gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:         gatewayData.gateway.Namespace,
					Name:              "http-route",
					CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
				},
				Spec: gatewayv1.HTTPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parentRef}},
					Hostnames:       []gatewayv1.Hostname{hostname},
					Rules: []gatewayv1.HTTPRouteRule{{
						BackendRefs: []gatewayv1.HTTPBackendRef{{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: gatewayv1.ObjectName("ripster"),
									Port: lo.ToPtr(gatewayv1.PortNumber(9180)),
								},
							},
						}},
						Matches: []gatewayv1.HTTPRouteMatch{{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  lo.ToPtr(gatewayv1.PathMatchPathPrefix),
								Value: new("/"),
							},
						}},
					}},
				},
			}

			k8sClient.EXPECT().List(t.Context(), &gatewayv1.HTTPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					list.(*gatewayv1.HTTPRouteList).Items = []gatewayv1.HTTPRoute{olderHTTPRoute}
					return nil
				})
			k8sClient.EXPECT().Status().Return(mockStatusWriter)
			var updatedRoute *gatewayv1.GRPCRoute
			mockStatusWriter.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				route, ok := obj.(*gatewayv1.GRPCRoute)
				if !ok {
					return false
				}
				updatedRoute = route
				parentStatus := route.Status.Parents[0]
				condition := meta.FindStatusCondition(parentStatus.Conditions, string(gatewayv1.RouteConditionAccepted))
				return condition != nil &&
					condition.Status == metav1.ConditionTrue &&
					condition.Reason == string(gatewayv1.RouteReasonAccepted)
			})).Return(nil)

			got, err := model.acceptRoute(t.Context(), resolvedGRPCRouteDetails{
				gatewayDetails:   gatewayData,
				grpcRoute:        currentRoute,
				matchedRef:       parentRef,
				matchedListeners: gatewayData.gateway.Spec.Listeners,
			})

			require.NoError(t, err)
			assert.Same(t, updatedRoute, got)
		})

		t.Run("returns existing route when already accepted for generation", func(t *testing.T) {
			gatewayData := makeResolvedGateway()
			parentRef := gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gatewayData.gateway.Name)}
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Generation = 42
				route.Status.Parents = []gatewayv1.RouteParentStatus{{
					ParentRef:      parentRef,
					ControllerName: ControllerClassName,
					Conditions: []metav1.Condition{{
						Type:               string(gatewayv1.RouteConditionAccepted),
						Status:             metav1.ConditionTrue,
						ObservedGeneration: 42,
					}},
				}}
			})
			model := newGRPCRouteModel(newMockDeps(t))

			got, err := model.acceptRoute(t.Context(), resolvedGRPCRouteDetails{
				gatewayDetails: gatewayData,
				grpcRoute:      route,
				matchedRef:     parentRef,
			})

			require.NoError(t, err)
			assert.Equal(t, &route, got)
		})

		t.Run("updates existing parent status", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			gatewayData := makeResolvedGateway()
			parentRef := gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gatewayData.gateway.Name)}
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Generation = 7
				route.Status.Parents = []gatewayv1.RouteParentStatus{{
					ParentRef:      parentRef,
					ControllerName: gatewayData.gatewayClass.Spec.ControllerName,
					Conditions: []metav1.Condition{{
						Type:               string(gatewayv1.RouteConditionAccepted),
						Status:             metav1.ConditionFalse,
						ObservedGeneration: 6,
					}},
				}}
			})

			k8sClient.EXPECT().Status().Return(mockStatusWriter)
			var updatedRoute *gatewayv1.GRPCRoute
			mockStatusWriter.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				var ok bool
				updatedRoute, ok = obj.(*gatewayv1.GRPCRoute)
				return ok
			})).Return(nil)

			got, err := model.acceptRoute(t.Context(), resolvedGRPCRouteDetails{
				gatewayDetails: gatewayData,
				grpcRoute:      route,
				matchedRef:     parentRef,
			})

			require.NoError(t, err)
			assert.Same(t, updatedRoute, got)
			require.Len(t, updatedRoute.Status.Parents, 1)
			condition := meta.FindStatusCondition(
				updatedRoute.Status.Parents[0].Conditions,
				string(gatewayv1.RouteConditionAccepted),
			)
			require.NotNil(t, condition)
			assert.Equal(t, metav1.ConditionTrue, condition.Status)
			assert.Equal(t, route.Generation, condition.ObservedGeneration)
		})

		t.Run("returns conflict list errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			k8sClient.EXPECT().List(t.Context(), &gatewayv1.HTTPRouteList{}).Return(wantErr)

			_, err := model.acceptRoute(t.Context(), resolvedGRPCRouteDetails{
				gatewayDetails:   makeResolvedGateway(),
				grpcRoute:        makeGRPCRoute(),
				matchedListeners: []gatewayv1.Listener{{Name: "grpc"}},
			})

			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("listHTTPRouteConflictCandidates", func(t *testing.T) {
		t.Run("filters deleted HTTPRoutes", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			activeRoute := makeRandomHTTPRoute()
			deletedRoute := makeRandomHTTPRoute()
			deletionTimestamp := metav1.Now()
			deletedRoute.DeletionTimestamp = &deletionTimestamp

			k8sClient.EXPECT().List(t.Context(), &gatewayv1.HTTPRouteList{}).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					list.(*gatewayv1.HTTPRouteList).Items = []gatewayv1.HTTPRoute{activeRoute, deletedRoute}
					return nil
				})

			got, err := model.listHTTPRouteConflictCandidates(t.Context())

			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, activeRoute.Name, got[0].identity.name)
		})
	})

	t.Run("rejectRoute", func(t *testing.T) {
		t.Run("sets rejected parent status", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			gatewayData := makeResolvedGateway()
			parentRef := gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gatewayData.gateway.Name)}
			route := makeGRPCRoute()
			wantMessage := faker.New().Lorem().Sentence(5)

			k8sClient.EXPECT().Status().Return(mockStatusWriter)
			mockStatusWriter.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				updatedRoute, ok := obj.(*gatewayv1.GRPCRoute)
				if !ok {
					return false
				}
				require.Len(t, updatedRoute.Status.Parents, 1)
				condition := meta.FindStatusCondition(
					updatedRoute.Status.Parents[0].Conditions,
					string(gatewayv1.RouteConditionAccepted),
				)
				return condition != nil &&
					condition.Status == metav1.ConditionFalse &&
					condition.Reason == string(routeReasonConflicted) &&
					condition.Message == wantMessage
			})).Return(nil)

			err := model.rejectRoute(t.Context(), resolvedGRPCRouteDetails{
				gatewayDetails: gatewayData,
				grpcRoute:      route,
				matchedRef:     parentRef,
			}, wantMessage)

			require.NoError(t, err)
		})

		t.Run("returns policy cleanup errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			fake := faker.New()
			listenerName := gatewayv1.SectionName("grpc")
			ruleName := "grpc-rule-" + fake.Lorem().Word()
			gatewayData := makeResolvedGateway(gatewayv1.Listener{Name: listenerName, Port: 50051})
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Annotations = map[string]string{
					GRPCRouteProgrammedPolicyRulesAnnotation: fmt.Sprintf("%s/%s", listenerName, ruleName),
				}
			})
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  gatewayData.config.Spec.LoadBalancerID,
				listenerName:    string(listenerName),
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{ruleName},
			}).Return(wantErr).Once()

			err := model.rejectRoute(t.Context(), resolvedGRPCRouteDetails{
				gatewayDetails:   gatewayData,
				grpcRoute:        route,
				matchedListeners: []gatewayv1.Listener{gatewayData.gateway.Spec.Listeners[0]},
			}, fake.Lorem().Sentence(5))

			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("resolveBackendRefs", func(t *testing.T) {
		t.Run("returns referenced services", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			backendRef := makeGRPCBackendRef()
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Spec.Rules = []gatewayv1.GRPCRouteRule{{BackendRefs: []gatewayv1.GRPCBackendRef{backendRef}}}
			})
			service := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: route.Namespace,
					Name:      string(backendRef.Name),
					UID:       apitypes.UID("svc-" + fake.UUID().V4()),
				},
			}
			k8sClient.EXPECT().Get(
				t.Context(),
				client.ObjectKey{Namespace: service.Namespace, Name: service.Name},
				mock.Anything,
			).RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
				*obj.(*corev1.Service) = service
				return nil
			}).Once()

			got, err := model.resolveBackendRefs(t.Context(), resolveGRPCBackendRefsParams{grpcRoute: route})

			require.NoError(t, err)
			assert.Equal(t, map[string]corev1.Service{service.Namespace + "/" + service.Name: service}, got)
		})

		t.Run("returns service lookup errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			backendRef := makeGRPCBackendRef()
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Spec.Rules = []gatewayv1.GRPCRouteRule{{BackendRefs: []gatewayv1.GRPCBackendRef{backendRef}}}
			})
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			k8sClient.EXPECT().Get(t.Context(), mock.Anything, mock.Anything).Return(wantErr)

			_, err := model.resolveBackendRefs(t.Context(), resolveGRPCBackendRefsParams{grpcRoute: route})

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("rejects cross namespace backend without reference grant", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			backendNamespace := gatewayv1.Namespace("backend-" + fake.Lorem().Word())
			backendRef := makeGRPCBackendRef(func(ref *gatewayv1.GRPCBackendRef) {
				ref.Namespace = &backendNamespace
			})
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Spec.Rules = []gatewayv1.GRPCRouteRule{{BackendRefs: []gatewayv1.GRPCBackendRef{backendRef}}}
			})

			k8sClient.EXPECT().
				List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					*list.(*gatewayv1beta1.ReferenceGrantList) = gatewayv1beta1.ReferenceGrantList{}
					return nil
				}).
				Once()

			_, err := model.resolveBackendRefs(t.Context(), resolveGRPCBackendRefsParams{grpcRoute: route})

			var statusErr grpcRouteStatusError
			require.ErrorAs(t, err, &statusErr)
			assert.Equal(t, gatewayv1.RouteConditionResolvedRefs, statusErr.conditionType)
			assert.Equal(t, gatewayv1.RouteReasonRefNotPermitted, statusErr.reason)
		})

		t.Run("returns reference grant lookup errors", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			backendNamespace := gatewayv1.Namespace("backend-" + fake.Lorem().Word())
			backendRef := makeGRPCBackendRef(func(ref *gatewayv1.GRPCBackendRef) {
				ref.Namespace = &backendNamespace
			})
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Spec.Rules = []gatewayv1.GRPCRouteRule{{BackendRefs: []gatewayv1.GRPCBackendRef{backendRef}}}
			})
			wantErr := errors.New(fake.Lorem().Sentence(10))

			k8sClient.EXPECT().
				List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
				Return(wantErr).
				Once()

			_, err := model.resolveBackendRefs(t.Context(), resolveGRPCBackendRefsParams{grpcRoute: route})

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("permits cross namespace backend with reference grant", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			backendNamespace := gatewayv1.Namespace("backend-" + fake.Lorem().Word())
			backendRef := makeGRPCBackendRef(func(ref *gatewayv1.GRPCBackendRef) {
				ref.Namespace = &backendNamespace
			})
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Spec.Rules = []gatewayv1.GRPCRouteRule{{BackendRefs: []gatewayv1.GRPCBackendRef{backendRef}}}
			})
			service := corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: string(backendNamespace),
					Name:      string(backendRef.Name),
					UID:       apitypes.UID("svc-" + fake.UUID().V4()),
				},
			}
			serviceName := gatewayv1.ObjectName(service.Name)
			grants := gatewayv1beta1.ReferenceGrantList{Items: []gatewayv1beta1.ReferenceGrant{{
				ObjectMeta: metav1.ObjectMeta{Namespace: service.Namespace, Name: "grant-" + fake.Lorem().Word()},
				Spec: gatewayv1beta1.ReferenceGrantSpec{
					From: []gatewayv1beta1.ReferenceGrantFrom{{
						Group:     gatewayv1.Group(gatewayAPIGroup),
						Kind:      gatewayv1.Kind("GRPCRoute"),
						Namespace: gatewayv1.Namespace(route.Namespace),
					}},
					To: []gatewayv1beta1.ReferenceGrantTo{{
						Group: "",
						Kind:  gatewayv1.Kind(serviceKind),
						Name:  &serviceName,
					}},
				},
			}}}

			k8sClient.EXPECT().
				List(t.Context(), mock.AnythingOfType("*v1beta1.ReferenceGrantList"), mock.Anything).
				RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
					*list.(*gatewayv1beta1.ReferenceGrantList) = grants
					return nil
				}).
				Once()
			k8sClient.EXPECT().
				Get(t.Context(), client.ObjectKey{Namespace: service.Namespace, Name: service.Name}, mock.Anything).
				RunAndReturn(func(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
					*obj.(*corev1.Service) = service
					return nil
				}).
				Once()

			got, err := model.resolveBackendRefs(t.Context(), resolveGRPCBackendRefsParams{grpcRoute: route})

			require.NoError(t, err)
			assert.Equal(t, map[string]corev1.Service{service.Namespace + "/" + service.Name: service}, got)
		})
	})

	t.Run("deprovisionRoute", func(t *testing.T) {
		t.Run("removes policy rules and finalizer", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			config := makeRandomGatewayConfig()
			listenerName := gatewayv1.SectionName("grpc")
			ruleName := "rule-" + fake.Lorem().Word()
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Annotations = map[string]string{
					GRPCRouteProgrammedPolicyRulesAnnotation: fmt.Sprintf("%s/%s", listenerName, ruleName),
				}
				controllerutil.AddFinalizer(route, GRPCRouteProgrammedFinalizer)
			})

			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  config.Spec.LoadBalancerID,
				listenerName:    string(listenerName),
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{ruleName},
			}).Return(nil).Once()
			k8sClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				updatedRoute, ok := obj.(*gatewayv1.GRPCRoute)
				return ok && !controllerutil.ContainsFinalizer(updatedRoute, GRPCRouteProgrammedFinalizer)
			})).Return(nil).Once()

			err := model.deprovisionRoute(t.Context(), deprovisionGRPCRouteParams{
				config:           config,
				grpcRoute:        route,
				matchedListeners: []gatewayv1.Listener{{Name: listenerName}},
			})

			require.NoError(t, err)
		})

		t.Run("deprovisions backend sets once per unique backend ref", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			config := makeRandomGatewayConfig()
			listenerName := gatewayv1.SectionName("grpc")
			ruleName := "rule-" + fake.Lorem().Word()
			backendRef := makeGRPCBackendRef()
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Annotations = map[string]string{
					GRPCRouteProgrammedPolicyRulesAnnotation: fmt.Sprintf("%s/%s", listenerName, ruleName),
				}
				route.Spec.Rules = []gatewayv1.GRPCRouteRule{
					{BackendRefs: []gatewayv1.GRPCBackendRef{backendRef}},
					{BackendRefs: []gatewayv1.GRPCBackendRef{backendRef}},
				}
				controllerutil.AddFinalizer(route, GRPCRouteProgrammedFinalizer)
			})

			commitCall := ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  config.Spec.LoadBalancerID,
				listenerName:    string(listenerName),
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{ruleName},
			}).Return(nil).Once()
			ociLBModel.EXPECT().deprovisionBackendSet(t.Context(), deprovisionBackendSetParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				routeNamespace: route.Namespace,
				backendRef:     backendRef.BackendRef,
			}).Return(nil).Once().NotBefore(commitCall)
			k8sClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				updatedRoute, ok := obj.(*gatewayv1.GRPCRoute)
				return ok && !controllerutil.ContainsFinalizer(updatedRoute, GRPCRouteProgrammedFinalizer)
			})).Return(nil).Once()

			err := model.deprovisionRoute(t.Context(), deprovisionGRPCRouteParams{
				config:           config,
				grpcRoute:        route,
				matchedListeners: []gatewayv1.Listener{{Name: listenerName}},
			})

			require.NoError(t, err)
		})

		t.Run("returns update errors", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			k8sClient, _ := deps.K8sClient.(*Mockk8sClient)
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				controllerutil.AddFinalizer(route, GRPCRouteProgrammedFinalizer)
			})
			wantErr := errors.New(faker.New().Lorem().Sentence(10))
			k8sClient.EXPECT().Update(t.Context(), mock.Anything).Return(wantErr).Once()

			err := model.deprovisionRoute(t.Context(), deprovisionGRPCRouteParams{
				config:    makeRandomGatewayConfig(),
				grpcRoute: route,
			})

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns routing policy cleanup errors", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			listenerName := gatewayv1.SectionName("grpc")
			ruleName := "rule-" + fake.Lorem().Word()
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Annotations = map[string]string{
					GRPCRouteProgrammedPolicyRulesAnnotation: fmt.Sprintf("%s/%s", listenerName, ruleName),
				}
			})
			config := makeRandomGatewayConfig()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  config.Spec.LoadBalancerID,
				listenerName:    string(listenerName),
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{ruleName},
			}).Return(wantErr).Once()

			err := model.deprovisionRoute(t.Context(), deprovisionGRPCRouteParams{
				config:           config,
				grpcRoute:        route,
				matchedListeners: []gatewayv1.Listener{{Name: listenerName}},
			})

			require.ErrorIs(t, err, wantErr)
		})

		t.Run("returns backend set cleanup errors", func(t *testing.T) {
			fake := faker.New()
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
			listenerName := gatewayv1.SectionName("grpc")
			ruleName := "rule-" + fake.Lorem().Word()
			backendRef := makeGRPCBackendRef()
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Annotations = map[string]string{
					GRPCRouteProgrammedPolicyRulesAnnotation: fmt.Sprintf("%s/%s", listenerName, ruleName),
				}
				route.Spec.Rules = []gatewayv1.GRPCRouteRule{{BackendRefs: []gatewayv1.GRPCBackendRef{backendRef}}}
			})
			config := makeRandomGatewayConfig()
			wantErr := errors.New(fake.Lorem().Sentence(10))

			commitCall := ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
				loadBalancerID:  config.Spec.LoadBalancerID,
				listenerName:    string(listenerName),
				policyRules:     []loadbalancer.RoutingRule{},
				prevPolicyRules: []string{ruleName},
			}).Return(nil).Once()
			ociLBModel.EXPECT().deprovisionBackendSet(t.Context(), deprovisionBackendSetParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				routeNamespace: route.Namespace,
				backendRef:     backendRef.BackendRef,
			}).Return(wantErr).Once().NotBefore(commitCall)

			err := model.deprovisionRoute(t.Context(), deprovisionGRPCRouteParams{
				config:           config,
				grpcRoute:        route,
				matchedListeners: []gatewayv1.Listener{{Name: listenerName}},
			})

			require.ErrorIs(t, err, wantErr)
		})
	})

	t.Run("isProgrammingRequired", func(t *testing.T) {
		t.Run("returns true when parent status is missing", func(t *testing.T) {
			model := newGRPCRouteModel(newMockDeps(t))

			got := model.isProgrammingRequired(resolvedGRPCRouteDetails{
				gatewayDetails: makeResolvedGateway(),
				grpcRoute:      makeGRPCRoute(),
				matchedRef:     gatewayv1.ParentReference{Name: "gw"},
			})

			assert.True(t, got)
		})

		t.Run("delegates condition check when parent status exists", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newGRPCRouteModel(deps)
			resourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
			gatewayData := makeResolvedGateway()
			parentRef := gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gatewayData.gateway.Name)}
			route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Status.Parents = []gatewayv1.RouteParentStatus{{
					ParentRef:      parentRef,
					ControllerName: ControllerClassName,
				}}
			})
			resourcesModel.EXPECT().isConditionSet(mock.MatchedBy(func(params isConditionSetParams) bool {
				return params.conditionType == string(gatewayv1.RouteConditionResolvedRefs) &&
					params.annotations[GRPCRouteProgrammingRevisionAnnotation] == GRPCRouteProgrammingRevisionValue &&
					params.annotations[L7RouteProgrammedLoadBalancerIDAnnotation] == gatewayData.config.Spec.LoadBalancerID
			})).Return(true).Once()

			got := model.isProgrammingRequired(resolvedGRPCRouteDetails{
				gatewayDetails: gatewayData,
				grpcRoute:      route,
				matchedRef:     parentRef,
			})

			assert.False(t, got)
		})
	})

	t.Run("programRoute", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newGRPCRouteModel(deps)
		ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
		backendRef := makeGRPCBackendRef()
		previousRuleName := "previous-grpc-rule-" + fake.Lorem().Word()
		route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
			service := fmt.Sprintf("%s.%s", fake.Lorem().Word(), fake.Lorem().Word())
			method := fake.Lorem().Word()
			route.Spec.Rules = []gatewayv1.GRPCRouteRule{{
				Matches: []gatewayv1.GRPCRouteMatch{{
					Method: &gatewayv1.GRPCMethodMatch{Service: &service, Method: &method},
				}},
				BackendRefs: []gatewayv1.GRPCBackendRef{backendRef},
			}}
			route.Annotations = map[string]string{
				GRPCRouteProgrammedPolicyRulesAnnotation: fmt.Sprintf("grpc/%s", previousRuleName),
			}
		})
		config := makeRandomGatewayConfig()
		listener := gatewayv1.Listener{Name: gatewayv1.SectionName("grpc"), Port: 50051}
		service := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: route.Namespace, Name: string(backendRef.Name)},
		}
		ruleName := "grpc_rule_" + fake.Lorem().Word()
		routingRule := loadbalancer.RoutingRule{Name: &ruleName}

		ociLBModel.EXPECT().reconcileBackendSet(t.Context(), reconcileBackendSetParams{
			loadBalancerID: config.Spec.LoadBalancerID,
			service:        service,
			routeNS:        route.Namespace,
			backendRef:     backendRef.BackendRef,
		}).Return(nil).Once()
		ociLBModel.EXPECT().makeGRPCRoutingRule(t.Context(), makeGRPCRoutingRuleParams{
			grpcRoute:          route,
			grpcRouteRuleIndex: 0,
		}).Return(routingRule, nil).Once()
		ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
			loadBalancerID:  config.Spec.LoadBalancerID,
			listenerName:    string(listener.Name),
			policyRules:     []loadbalancer.RoutingRule{routingRule},
			prevPolicyRules: []string{previousRuleName},
		}).Return(nil).Once()

		got, err := model.programRoute(t.Context(), programGRPCRouteParams{
			config:           config,
			grpcRoute:        route,
			knownBackends:    map[string]corev1.Service{service.Namespace + "/" + service.Name: service},
			matchedListeners: []gatewayv1.Listener{listener},
		})

		require.NoError(t, err)
		assert.Equal(
			t,
			[]string{fmt.Sprintf("%s/%s", listener.Name, lo.FromPtr(routingRule.Name))},
			got.programmedPolicyRules,
		)
	})

	t.Run("programRoute clears backend SSL config when BackendTLSPolicy no longer matches", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newGRPCRouteModel(deps)
		model.backendTLSPolicy = &stubBackendTLSPolicyModel{resolveErr: errBackendTLSPolicyNotFound}
		ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
		config := makeRandomGatewayConfig()
		backendRef := makeGRPCBackendRef()
		listener := gatewayv1.Listener{Name: gatewayv1.SectionName("grpc"), Port: 50051}
		route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
			route.Spec.Rules = []gatewayv1.GRPCRouteRule{{
				BackendRefs: []gatewayv1.GRPCBackendRef{backendRef},
			}}
		})
		service := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: route.Namespace, Name: string(backendRef.Name)},
		}
		ruleName := "grpc_rule_" + fake.Lorem().Word()
		routingRule := loadbalancer.RoutingRule{Name: &ruleName}

		ociLBModel.EXPECT().
			reconcileBackendSet(t.Context(), mock.MatchedBy(func(params reconcileBackendSetParams) bool {
				return params.loadBalancerID == config.Spec.LoadBalancerID &&
					params.service.Name == service.Name &&
					params.backendRef.Name == backendRef.Name &&
					params.manageSSLConfig &&
					params.sslConfig == nil
			})).
			Return(nil).
			Once()
		ociLBModel.EXPECT().makeGRPCRoutingRule(t.Context(), makeGRPCRoutingRuleParams{
			grpcRoute:          route,
			grpcRouteRuleIndex: 0,
		}).Return(routingRule, nil).Once()
		ociLBModel.EXPECT().commitRoutingPolicy(t.Context(), commitRoutingPolicyParams{
			loadBalancerID: config.Spec.LoadBalancerID,
			listenerName:   string(listener.Name),
			policyRules:    []loadbalancer.RoutingRule{routingRule},
		}).Return(nil).Once()

		_, err := model.programRoute(t.Context(), programGRPCRouteParams{
			config:           config,
			grpcRoute:        route,
			knownBackends:    map[string]corev1.Service{service.Namespace + "/" + service.Name: service},
			matchedListeners: []gatewayv1.Listener{listener},
		})

		require.NoError(t, err)
	})

	t.Run("ensureGRPCListenersProtocol updates matched listeners to HTTP2", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newGRPCRouteModel(deps)
		ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
		config := makeRandomGatewayConfig()
		listeners := []gatewayv1.Listener{
			{Name: gatewayv1.SectionName("grpc-" + fake.Lorem().Word()), Port: gatewayv1.PortNumber(443)},
			{Name: gatewayv1.SectionName("grpc-" + fake.Lorem().Word()), Port: gatewayv1.PortNumber(8443)},
		}

		for _, listener := range listeners {
			ociLBModel.EXPECT().ensureHTTP2ListenerProtocol(t.Context(), ensureHTTP2ListenerProtocolParams{
				loadBalancerID: config.Spec.LoadBalancerID,
				listenerName:   string(listener.Name),
			}).Return(nil).Once()
		}

		err := model.ensureGRPCListenersProtocol(t.Context(), ensureGRPCListenersProtocolParams{
			config:           config,
			matchedListeners: listeners,
		})

		require.NoError(t, err)
	})

	t.Run("ensureGRPCListenersProtocol returns listener protocol update errors", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newGRPCRouteModel(deps)
		ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
		config := makeRandomGatewayConfig()
		listener := gatewayv1.Listener{Name: gatewayv1.SectionName("grpc"), Port: 50051}
		wantErr := errors.New(fake.Lorem().Sentence(10))

		ociLBModel.EXPECT().ensureHTTP2ListenerProtocol(t.Context(), ensureHTTP2ListenerProtocolParams{
			loadBalancerID: config.Spec.LoadBalancerID,
			listenerName:   string(listener.Name),
		}).Return(wantErr).Once()

		err := model.ensureGRPCListenersProtocol(t.Context(), ensureGRPCListenersProtocolParams{
			config:           config,
			matchedListeners: []gatewayv1.Listener{listener},
		})

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("programRoute returns routing rule errors", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newGRPCRouteModel(deps)
		ociLBModel, _ := deps.OciLBModel.(*MockociLoadBalancerModel)
		backendRef := makeGRPCBackendRef()
		route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
			route.Spec.Rules = []gatewayv1.GRPCRouteRule{{BackendRefs: []gatewayv1.GRPCBackendRef{backendRef}}}
		})
		config := makeRandomGatewayConfig()
		service := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: route.Namespace, Name: string(backendRef.Name)},
		}
		wantErr := errors.New(fake.Lorem().Sentence(10))

		ociLBModel.EXPECT().reconcileBackendSet(t.Context(), mock.Anything).Return(nil).Once()
		ociLBModel.EXPECT().
			makeGRPCRoutingRule(t.Context(), mock.Anything).
			Return(loadbalancer.RoutingRule{}, wantErr).
			Once()

		_, err := model.programRoute(t.Context(), programGRPCRouteParams{
			config:        config,
			grpcRoute:     route,
			knownBackends: map[string]corev1.Service{service.Namespace + "/" + service.Name: service},
		})

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("setProgrammed", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newGRPCRouteModel(deps)
		resourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
		gatewayClass := gatewayv1.GatewayClass{
			Spec: gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
		}
		gateway := gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw-" + fake.Lorem().Word()}}
		config := makeRandomGatewayConfig()
		parentRef := gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gateway.Name)}
		route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
			route.Status.Parents = []gatewayv1.RouteParentStatus{{
				ParentRef:      parentRef,
				ControllerName: ControllerClassName,
			}}
		})
		programmedRules := []string{"grpc/rule-" + fake.Lorem().Word()}

		resourcesModel.EXPECT().setCondition(t.Context(), mock.MatchedBy(func(params setConditionParams) bool {
			return params.conditionType == string(gatewayv1.RouteConditionResolvedRefs) &&
				params.finalizer == GRPCRouteProgrammedFinalizer &&
				params.annotations[GRPCRouteProgrammingRevisionAnnotation] == GRPCRouteProgrammingRevisionValue &&
				params.annotations[GRPCRouteProgrammedPolicyRulesAnnotation] == strings.Join(programmedRules, ",") &&
				params.annotations[L7RouteProgrammedLoadBalancerIDAnnotation] == config.Spec.LoadBalancerID
		})).Return(nil).Once()

		err := model.setProgrammed(t.Context(), setGRPCRouteProgrammedParams{
			grpcRoute:             route,
			gatewayClass:          gatewayClass,
			gateway:               gateway,
			config:                config,
			matchedRef:            parentRef,
			programmedPolicyRules: programmedRules,
		})

		require.NoError(t, err)
	})

	t.Run("setRejected sets resolved refs condition false", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newGRPCRouteModel(deps)
		resourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
		gatewayData := makeResolvedGateway()
		parentRef := gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gatewayData.gateway.Name)}
		route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
			route.Status.Parents = []gatewayv1.RouteParentStatus{{
				ParentRef:      parentRef,
				ControllerName: ControllerClassName,
			}}
		})
		statusErr := newGRPCRouteRefNotPermittedStatusError(fake.Lorem().Sentence(8))

		resourcesModel.EXPECT().setCondition(t.Context(), mock.MatchedBy(func(params setConditionParams) bool {
			return params.conditionType == string(gatewayv1.RouteConditionResolvedRefs) &&
				params.status == metav1.ConditionFalse &&
				params.reason == string(gatewayv1.RouteReasonRefNotPermitted) &&
				params.message == statusErr.message
		})).Return(nil).Once()

		err := model.setRejected(t.Context(), resolvedGRPCRouteDetails{
			gatewayDetails: gatewayData,
			grpcRoute:      route,
			matchedRef:     parentRef,
		}, statusErr)

		require.NoError(t, err)
	})

	t.Run("setRejected returns status update errors", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newGRPCRouteModel(deps)
		resourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
		gatewayData := makeResolvedGateway()
		parentRef := gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gatewayData.gateway.Name)}
		route := makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
			route.Status.Parents = []gatewayv1.RouteParentStatus{{
				ParentRef:      parentRef,
				ControllerName: ControllerClassName,
			}}
		})
		wantErr := errors.New(fake.Lorem().Sentence(10))

		resourcesModel.EXPECT().setCondition(t.Context(), mock.Anything).Return(wantErr).Once()

		err := model.setRejected(t.Context(), resolvedGRPCRouteDetails{
			gatewayDetails: gatewayData,
			grpcRoute:      route,
			matchedRef:     parentRef,
		}, newGRPCRouteRefNotPermittedStatusError(fake.Lorem().Sentence(8)))

		require.ErrorIs(t, err, wantErr)
	})

	t.Run("setProgrammed returns status update errors", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newGRPCRouteModel(deps)
		resourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
		gateway := gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gw-" + fake.Lorem().Word()}}
		wantErr := errors.New(fake.Lorem().Sentence(10))
		resourcesModel.EXPECT().setCondition(t.Context(), mock.Anything).Return(wantErr).Once()

		err := model.setProgrammed(t.Context(), setGRPCRouteProgrammedParams{
			grpcRoute: makeGRPCRoute(func(route *gatewayv1.GRPCRoute) {
				route.Status.Parents = []gatewayv1.RouteParentStatus{{
					ParentRef:      gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gateway.Name)},
					ControllerName: ControllerClassName,
				}}
			}),
			gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName}},
			gateway:      gateway,
			matchedRef:   gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gateway.Name)},
		})

		require.ErrorIs(t, err, wantErr)
	})
}

func TestGRPCRouteStatusError(t *testing.T) {
	message := "grpc route rejected"
	err := newGRPCRouteRefNotPermittedStatusError(message)

	assert.Equal(t, message, err.Error())
	assert.Equal(t, gatewayv1.RouteConditionResolvedRefs, err.conditionType)
	assert.Equal(t, gatewayv1.RouteReasonRefNotPermitted, err.reason)
}
