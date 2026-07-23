package app

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
	"github.com/gemyago/oke-gateway-api/internal/types"
)

func TestUDPRouteModel(t *testing.T) {
	t.Run("udpBackendsEqual detects port changes", func(t *testing.T) {
		assert.False(t, udpBackendsEqual(
			[]networkloadbalancer.Backend{{
				IpAddress: new("10.0.0.10"),
				Port:      new(5684),
				IsDrain:   new(false),
				Weight:    new(1),
			}},
			[]networkloadbalancer.BackendDetails{{
				IpAddress: new("10.0.0.10"),
				Port:      new(8080),
				IsDrain:   new(false),
				Weight:    new(1),
			}},
		))
	})

	t.Run("desired backend sets ignore non gateway parent refs", func(t *testing.T) {
		otherGroup := gatewayv1.Group("example.com")
		details := resolvedUDPRouteDetails{
			udpRoute: gatewayv1.UDPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
				Spec: gatewayv1.UDPRouteSpec{
					CommonRouteSpec: gatewayv1.CommonRouteSpec{
						ParentRefs: []gatewayv1.ParentReference{
							{Group: &otherGroup, Name: "edge"},
							{Name: "other"},
						},
					},
				},
			},
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
					Spec: gatewayv1.GatewaySpec{
						Listeners: []gatewayv1.Listener{
							{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: 5684},
						},
					},
				},
			},
		}

		assert.Empty(t, desiredUDPRouteBackendSetNames(details))
	})

	t.Run("resolveRequest wraps Kubernetes read errors", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "iot",
				Name:      "coap",
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation: "5684",
				},
			},
			Spec: gatewayv1.UDPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
			}},
		}
		for name, tc := range map[string]struct {
			setup func(*Mockk8sClient)
			err   string
		}{
			"route": {
				setup: func(mockClient *Mockk8sClient) {
					mockClient.EXPECT().
						Get(
							t.Context(),
							apitypes.NamespacedName{Namespace: "iot", Name: "coap"},
							mock.AnythingOfType("*v1.UDPRoute"),
						).
						Return(errors.New("route failed"))
				},
				err: "failed to get UDPRoute",
			},
			"gateway": {
				setup: func(mockClient *Mockk8sClient) {
					mockClient.EXPECT().
						Get(
							t.Context(),
							apitypes.NamespacedName{Namespace: "iot", Name: "coap"},
							mock.AnythingOfType("*v1.UDPRoute"),
						).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							*(mustUDPRoute(t, obj)) = route
							return nil
						})
					mockClient.EXPECT().
						Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "edge"}, mock.AnythingOfType("*v1.Gateway")).
						Return(errors.New("gateway failed"))
				},
				err: "failed to get Gateway",
			},
			"gateway class": {
				setup: func(mockClient *Mockk8sClient) {
					mockClient.EXPECT().
						Get(
							t.Context(),
							apitypes.NamespacedName{Namespace: "iot", Name: "coap"},
							mock.AnythingOfType("*v1.UDPRoute"),
						).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							*(mustUDPRoute(t, obj)) = route
							return nil
						})
					mockClient.EXPECT().
						Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "edge"}, mock.AnythingOfType("*v1.Gateway")).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							gateway := mustGateway(t, obj)
							gateway.Namespace = "iot"
							gateway.Name = "edge"
							gateway.Spec.GatewayClassName = "oke-nlb"
							return nil
						})
					mockClient.EXPECT().
						Get(t.Context(), apitypes.NamespacedName{Name: "oke-nlb"}, mock.AnythingOfType("*v1.GatewayClass")).
						Return(errors.New("gateway class failed"))
				},
				err: "failed to get GatewayClass",
			},
			"config": {
				setup: func(mockClient *Mockk8sClient) {
					mockClient.EXPECT().
						Get(
							t.Context(),
							apitypes.NamespacedName{Namespace: "iot", Name: "coap"},
							mock.AnythingOfType("*v1.UDPRoute"),
						).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							*(mustUDPRoute(t, obj)) = route
							return nil
						})
					mockClient.EXPECT().
						Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "edge"}, mock.AnythingOfType("*v1.Gateway")).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							gateway := mustGateway(t, obj)
							gateway.Namespace = "iot"
							gateway.Name = "edge"
							gateway.Spec.GatewayClassName = "oke-nlb"
							gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
								ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
							}
							return nil
						})
					mockClient.EXPECT().
						Get(t.Context(), apitypes.NamespacedName{Name: "oke-nlb"}, mock.AnythingOfType("*v1.GatewayClass")).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							gatewayClass := mustGatewayClass(t, obj)
							gatewayClass.Name = "oke-nlb"
							gatewayClass.Spec.ControllerName = gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName)
							return nil
						})
					mockClient.EXPECT().
						Get(
							t.Context(),
							apitypes.NamespacedName{Namespace: "iot", Name: "nlb-config"},
							mock.AnythingOfType("*types.GatewayConfig"),
						).
						Return(errors.New("config failed"))
				},
				err: "failed to get GatewayConfig",
			},
		} {
			t.Run(name, func(t *testing.T) {
				mockClient := NewMockk8sClient(t)
				tc.setup(mockClient)
				model := newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})

				_, err := model.resolveRequest(t.Context(), reconcile.Request{
					NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"},
				})

				require.ErrorContains(t, err, tc.err)
			})
		}
	})

	t.Run("resolveRequest returns status update errors for unmatched listener", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
			Spec: gatewayv1.UDPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
			}},
		}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "coap"}, mock.AnythingOfType("*v1.UDPRoute")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				*(mustUDPRoute(t, obj)) = route
				return nil
			})
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "edge"}, mock.AnythingOfType("*v1.Gateway")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				gateway := mustGateway(t, obj)
				gateway.Namespace = "iot"
				gateway.Name = "edge"
				gateway.Spec.GatewayClassName = "oke-nlb"
				gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
					ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
				}
				gateway.Spec.Listeners = []gatewayv1.Listener{
					{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935},
				}
				return nil
			})
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Name: "oke-nlb"}, mock.AnythingOfType("*v1.GatewayClass")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				gatewayClass := mustGatewayClass(t, obj)
				gatewayClass.Name = "oke-nlb"
				gatewayClass.Spec.ControllerName = gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName)
				return nil
			})
		mockClient.EXPECT().
			Get(
				t.Context(),
				apitypes.NamespacedName{Namespace: "iot", Name: "nlb-config"},
				mock.AnythingOfType("*types.GatewayConfig"),
			).
			Return(nil)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			Return(errors.New("status failed"))
		model := newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})

		_, err := model.resolveRequest(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"},
		})

		require.ErrorContains(t, err, "failed to update UDPRoute coap status")
	})

	t.Run("resolveRequest removes finalizer from deleting route with no resolved parent", func(t *testing.T) {
		now := metav1.Now()
		otherGroup := gatewayv1.Group("example.com")
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "iot",
				Name:              "coap",
				DeletionTimestamp: &now,
				Finalizers:        []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation: "bs_coap",
				},
			},
			Spec: gatewayv1.UDPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Group: &otherGroup, Name: "edge"}},
			}},
		}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "coap"}, mock.AnythingOfType("*v1.UDPRoute")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				*(mustUDPRoute(t, obj)) = route
				return nil
			})
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				updated := mustUDPRoute(t, obj)
				assert.NotContains(t, updated.Finalizers, NetworkLoadBalancerUDPRouteProgrammedFinalizer)
				assert.NotContains(t, updated.Annotations, NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation)
				return nil
			})
		model := newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})

		resolved, err := model.resolveRequest(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "coap"},
		})

		require.NoError(t, err)
		assert.Empty(t, resolved)
	})

	t.Run("programRoute rejects route when listener is already owned", func(t *testing.T) {
		currentRoute := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "z-route",
				Generation: 1,
			},
			Spec: gatewayv1.UDPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{
							Name:        "edge",
							SectionName: lo.ToPtr(gatewayv1.SectionName("coap")),
						},
					},
				},
			},
		}
		otherRoute := currentRoute
		otherRoute.Name = "a-route"

		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1.UDPRouteList")).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1.UDPRouteList{
					Items: []gatewayv1.UDPRoute{currentRoute, otherRoute},
				}))
				return nil
			})

		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})

		err := model.programRoute(t.Context(), resolvedUDPRouteDetails{
			udpRoute: currentRoute,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "iot",
						Name:      "edge",
					},
				},
			},
			matchedListener: gatewayv1.Listener{
				Name:     "coap",
				Protocol: gatewayv1.UDPProtocolType,
				Port:     5684,
			},
		})

		var statusErr udpRouteStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, gatewayv1.RouteReasonNotAllowedByListeners, statusErr.reason)
		assert.Equal(t, "listener coap already has an attached UDPRoute iot/a-route", statusErr.message)
	})

	t.Run("listener ownership helpers return list errors", func(t *testing.T) {
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1.UDPRouteList")).
			Return(errors.New("list failed")).
			Twice()
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})
		modelImpl := mustUDPRouteModelImpl(t, model)
		details := resolvedUDPRouteDetails{}
		err := modelImpl.ensureExclusiveListenerOwner(t.Context(), details)
		require.ErrorContains(t, err, "failed to list UDPRoutes for listener ownership check")
		_, err = modelImpl.nextEligibleRouteForListener(t.Context(), details)
		require.ErrorContains(t, err, "failed to list UDPRoutes for listener failover")
	})

	t.Run("listener ownership ignores non matching parent refs", func(t *testing.T) {
		serviceKind := gatewayv1.Kind("Service")
		routes := gatewayv1.UDPRouteList{Items: []gatewayv1.UDPRoute{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "service-parent"},
				Spec: gatewayv1.UDPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Kind: &serviceKind, Name: "backend"}},
				}},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "other-gateway"},
				Spec: gatewayv1.UDPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "other"}},
				}},
			},
		}}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1.UDPRouteList")).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(routes))
				return nil
			})
		model := newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		modelImpl := mustUDPRouteModelImpl(t, model)
		err := modelImpl.ensureExclusiveListenerOwner(t.Context(), resolvedUDPRouteDetails{
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			},
			udpRoute:        gatewayv1.UDPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"}},
			matchedListener: gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: 5684},
		})
		require.NoError(t, err)
	})

	t.Run("deprovisionRoute clears backend set and removes finalizer when no successor exists", func(t *testing.T) {
		currentRoute := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
			},
			Spec: gatewayv1.UDPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				},
			},
		}

		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1.UDPRouteList")).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1.UDPRouteList{
					Items: []gatewayv1.UDPRoute{currentRoute},
				}))
				return nil
			})
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				assert.NotContains(t, obj.GetFinalizers(), NetworkLoadBalancerUDPRouteProgrammedFinalizer)
				assert.NotContains(t, obj.GetAnnotations(), NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation)
				return nil
			})

		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
		})

		err := model.deprovisionRoute(t.Context(), resolvedUDPRouteDetails{
			udpRoute: currentRoute,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "iot",
						Name:      "edge",
					},
				},
			},
			matchedListener: gatewayv1.Listener{
				Name:     "coap",
				Protocol: gatewayv1.UDPProtocolType,
				Port:     5684,
			},
		})

		require.NoError(t, err)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		request := nlbClient.updateBackendSetRequests[0]
		assert.Equal(t, "nlb-id", lo.FromPtr(request.NetworkLoadBalancerId))
		assert.Equal(t, "bs_coap", lo.FromPtr(request.BackendSetName))
		assert.False(t, lo.FromPtr(request.UpdateBackendSetDetails.IsPreserveSource))
		assert.Empty(t, request.UpdateBackendSetDetails.Backends)
	})

	t.Run("endpointBackendsForRoute rejects invalid and unavailable backends", func(t *testing.T) {
		port := gatewayv1.PortNumber(5684)
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
		})
		modelImpl := mustUDPRouteModelImpl(t, model)
		_, err := modelImpl.endpointBackendsForRoute(t.Context(), gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
			Spec: gatewayv1.UDPRouteSpec{
				Rules: []gatewayv1.UDPRouteRule{
					{
						BackendRefs: []gatewayv1.BackendRef{
							{BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend"}},
						},
					},
				},
			},
		})
		var statusErr udpRouteStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, gatewayv1.RouteReasonInvalidKind, statusErr.reason)

		backendNamespace := gatewayv1.Namespace("other")
		_, err = modelImpl.endpointBackendsForRoute(t.Context(), gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
			Spec: gatewayv1.UDPRouteSpec{
				Rules: []gatewayv1.UDPRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Namespace: &backendNamespace,
							Name:      "backend",
							Port:      &port,
						},
					}},
				}},
			},
		})
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, gatewayv1.RouteReasonRefNotPermitted, statusErr.reason)

		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "backend"}, mock.AnythingOfType("*v1.Service")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				service := mustService(t, obj)
				service.Namespace = "iot"
				service.Name = "backend"
				service.Spec.Ports = []corev1.ServicePort{{Port: port}}
				return nil
			})
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1.EndpointSliceList"), mock.Anything, mock.Anything).
			Return(errors.New("list failed"))
		model = newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		modelImpl = mustUDPRouteModelImpl(t, model)
		_, err = modelImpl.endpointBackendsForRoute(t.Context(), gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
			Spec: gatewayv1.UDPRouteSpec{
				Rules: []gatewayv1.UDPRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					}},
				}},
			},
		})
		require.ErrorContains(t, err, "failed to list endpoint slices")

		mockClient = NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "backend"}, mock.AnythingOfType("*v1.Service")).
			Return(errors.New("get failed"))
		model = newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		modelImpl = mustUDPRouteModelImpl(t, model)
		_, err = modelImpl.endpointBackendsForRoute(t.Context(), gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap"},
			Spec: gatewayv1.UDPRouteSpec{
				Rules: []gatewayv1.UDPRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					}},
				}},
			},
		})
		require.ErrorContains(t, err, "failed to get service")
	})

	t.Run("setProgrammed adds finalizer and backend set annotation", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Generation: 1,
			},
			Spec: gatewayv1.UDPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: "edge", SectionName: lo.ToPtr(gatewayv1.SectionName("coap"))},
					},
				},
			},
		}

		mockClient := NewMockk8sClient(t)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			Return(nil)
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				assert.Contains(t, obj.GetFinalizers(), NetworkLoadBalancerUDPRouteProgrammedFinalizer)
				assert.Equal(
					t,
					"bs_coap",
					obj.GetAnnotations()[NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation],
				)
				assert.Equal(
					t,
					"nlb-id",
					obj.GetAnnotations()[L4RouteProgrammedNetworkLoadBalancerIDAnnotation],
				)
				return nil
			})

		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})

		err := model.setProgrammed(t.Context(), resolvedUDPRouteDetails{
			udpRoute: route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
					Spec: gatewayv1.GatewaySpec{
						Listeners: []gatewayv1.Listener{
							{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: 5684},
						},
					},
				},
				config: types.GatewayConfig{Spec: types.GatewayConfigSpec{LoadBalancerID: "nlb-id"}},
			},
			matchedRef: gatewayv1.ParentReference{
				Name: "edge",
			},
			matchedListener: gatewayv1.Listener{
				Name:     "coap",
				Protocol: gatewayv1.UDPProtocolType,
				Port:     5684,
			},
		})

		require.NoError(t, err)
	})

	t.Run("setProgrammed updates existing parent status and wraps errors", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Generation: 2,
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation: "bs_coap",
				},
			},
			Status: gatewayv1.UDPRouteStatus{
				RouteStatus: gatewayv1.RouteStatus{
					Parents: []gatewayv1.RouteParentStatus{{
						ParentRef:      gatewayv1.ParentReference{Name: "edge"},
						ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
					}},
				},
			},
		}
		details := resolvedUDPRouteDetails{
			udpRoute: route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
					Spec: gatewayv1.GatewaySpec{Listeners: []gatewayv1.Listener{
						{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: 5684},
					}},
				},
			},
			matchedRef:      gatewayv1.ParentReference{Name: "edge"},
			matchedListener: gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: 5684},
		}

		mockClient := NewMockk8sClient(t)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.SubResourceUpdateOption) error {
				updated := mustUDPRoute(t, obj)
				require.Len(t, updated.Status.Parents, 1)
				assert.Len(t, updated.Status.Parents[0].Conditions, 2)
				return nil
			})
		model := newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		require.NoError(t, model.setProgrammed(t.Context(), details))

		mockClient = NewMockk8sClient(t)
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			Return(errors.New("update failed"))
		model = newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		details.udpRoute.Finalizers = nil
		details.udpRoute.Annotations = nil
		err := model.setProgrammed(t.Context(), details)
		require.ErrorContains(t, err, "failed to update UDPRoute iot/coap finalizer and annotations")

		mockClient = NewMockk8sClient(t)
		mockStatusWriter = k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			Return(errors.New("status failed"))
		model = newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		details.udpRoute = route
		err = model.setProgrammed(t.Context(), details)
		require.ErrorContains(t, err, "failed to update UDPRoute coap status")
	})

	t.Run("deprovisionDetachedRoute clears annotated backend set and removes finalizer", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation: "bs_old",
					L4RouteProgrammedNetworkLoadBalancerIDAnnotation:           "nlb-id",
				},
			},
			Status: gatewayv1.UDPRouteStatus{
				RouteStatus: gatewayv1.RouteStatus{
					Parents: []gatewayv1.RouteParentStatus{
						{
							ParentRef:      gatewayv1.ParentReference{Name: "edge"},
							ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
						},
					},
				},
			},
		}

		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), mock.Anything, mock.Anything).
			RunAndReturn(func(_ context.Context, key apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				switch typed := obj.(type) {
				case *gatewayv1.Gateway:
					assert.Equal(t, apitypes.NamespacedName{Namespace: "iot", Name: "edge"}, key)
					typed.Namespace = "iot"
					typed.Name = "edge"
					typed.Spec.GatewayClassName = "oke-nlb"
					typed.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
						ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
					}
				case *gatewayv1.GatewayClass:
					assert.Equal(t, apitypes.NamespacedName{Name: "oke-nlb"}, key)
					typed.Name = "oke-nlb"
					typed.Spec.ControllerName = gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName)
				case *types.GatewayConfig:
					assert.Equal(t, apitypes.NamespacedName{Namespace: "iot", Name: "nlb-config"}, key)
					typed.Namespace = "iot"
					typed.Name = "nlb-config"
				default:
					t.Fatalf("unexpected Get object type %T", obj)
				}
				return nil
			})
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				assert.NotContains(t, obj.GetFinalizers(), NetworkLoadBalancerUDPRouteProgrammedFinalizer)
				assert.NotContains(t, obj.GetAnnotations(), NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation)
				assert.NotContains(t, obj.GetAnnotations(), L4RouteProgrammedNetworkLoadBalancerIDAnnotation)
				return nil
			})

		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_old": {
							Name: new("bs_old"),
							HealthChecker: &networkloadbalancer.HealthChecker{
								Protocol: networkloadbalancer.HealthCheckProtocolsUdp,
								Port:     new(5684),
							},
						},
					},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
		})

		modelImpl := mustUDPRouteModelImpl(t, model)
		err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

		require.NoError(t, err)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		request := nlbClient.updateBackendSetRequests[0]
		assert.Equal(t, "nlb-id", lo.FromPtr(request.NetworkLoadBalancerId))
		assert.Equal(t, "bs_old", lo.FromPtr(request.BackendSetName))
		assert.False(t, lo.FromPtr(request.UpdateBackendSetDetails.IsPreserveSource))
		assert.Empty(t, request.UpdateBackendSetDetails.Backends)
		assert.Equal(
			t,
			networkloadbalancer.HealthCheckProtocolsTcp,
			request.UpdateBackendSetDetails.HealthChecker.Protocol,
		)
		assert.Equal(t, 5684, lo.FromPtr(request.UpdateBackendSetDetails.HealthChecker.Port))
		assert.Empty(t, request.UpdateBackendSetDetails.HealthChecker.RequestData)
		assert.Empty(t, request.UpdateBackendSetDetails.HealthChecker.ResponseData)
	})

	t.Run("deprovisionDetachedRoute clears annotated backend set by load balancer id", func(t *testing.T) {
		listenerSetKind := gatewayv1.Kind("ListenerSet")
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation: "bs_listener_set",
					L4RouteProgrammedNetworkLoadBalancerIDAnnotation:           "nlb-id",
				},
			},
			Status: gatewayv1.UDPRouteStatus{
				RouteStatus: gatewayv1.RouteStatus{
					Parents: []gatewayv1.RouteParentStatus{{
						ParentRef: gatewayv1.ParentReference{
							Kind: &listenerSetKind,
							Name: "extra",
						},
						ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
					}},
				},
			},
		}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(
				t.Context(),
				apitypes.NamespacedName{Namespace: "iot", Name: "extra"},
				mock.AnythingOfType("*v1.Gateway"),
			).
			Return(apierrors.NewNotFound(schema.GroupResource{
				Group:    gatewayv1.GroupName,
				Resource: "gateways",
			}, "extra"))
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				assert.NotContains(t, obj.GetFinalizers(), NetworkLoadBalancerUDPRouteProgrammedFinalizer)
				assert.NotContains(t, obj.GetAnnotations(), NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation)
				assert.NotContains(t, obj.GetAnnotations(), L4RouteProgrammedNetworkLoadBalancerIDAnnotation)
				return nil
			})
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_listener_set": {
							Name: new("bs_listener_set"),
							HealthChecker: &networkloadbalancer.HealthChecker{
								Protocol: networkloadbalancer.HealthCheckProtocolsTcp,
							},
						},
					},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
		})
		modelImpl := mustUDPRouteModelImpl(t, model)

		err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

		require.NoError(t, err)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		request := nlbClient.updateBackendSetRequests[0]
		assert.Equal(t, "nlb-id", lo.FromPtr(request.NetworkLoadBalancerId))
		assert.Equal(t, "bs_listener_set", lo.FromPtr(request.BackendSetName))
		assert.Empty(t, request.UpdateBackendSetDetails.Backends)
	})

	t.Run("deprovisionDetachedRoute returns annotated load balancer cleanup update errors", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation: "bs_listener_set",
					L4RouteProgrammedNetworkLoadBalancerIDAnnotation:           "nlb-id",
				},
			},
		}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			Return(errors.New("update failed"))
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_listener_set": {Name: new("bs_listener_set")},
					},
				},
			},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
		})
		modelImpl := mustUDPRouteModelImpl(t, model)

		err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

		require.ErrorContains(t, err, "failed to update detached UDPRoute")
	})

	t.Run("deprovisionDetachedRoute returns annotated load balancer cleanup errors", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation: "bs_listener_set",
					L4RouteProgrammedNetworkLoadBalancerIDAnnotation:           "nlb-id",
				},
			},
		}
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger:               diag.RootTestLogger(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{err: errors.New("nlb failed")},
		})
		modelImpl := mustUDPRouteModelImpl(t, model)

		err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

		require.ErrorContains(t, err, "nlb failed")
	})

	t.Run("deprovisionDetachedRoute removes finalizer when no backend sets are annotated", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
			},
		}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				assert.NotContains(t, obj.GetFinalizers(), NetworkLoadBalancerUDPRouteProgrammedFinalizer)
				return nil
			})
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})
		modelImpl := mustUDPRouteModelImpl(t, model)

		err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

		require.NoError(t, err)
	})

	t.Run("deprovisionDetachedRoute returns finalizer update error "+
		"when no backend sets are annotated", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
			},
		}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			Return(errors.New("update failed"))
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})
		modelImpl := mustUDPRouteModelImpl(t, model)

		err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

		require.ErrorContains(t, err, "failed to remove finalizer from detached UDPRoute")
	})

	t.Run("deprovisionDetachedRoute returns cleanup and update errors", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation: "bs_old",
				},
			},
			Status: gatewayv1.UDPRouteStatus{
				RouteStatus: gatewayv1.RouteStatus{
					Parents: []gatewayv1.RouteParentStatus{{
						ParentRef:      gatewayv1.ParentReference{Name: "edge"},
						ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
					}},
				},
			},
		}
		gatewayObjects := []client.Object{
			&gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: "oke-nlb",
					Infrastructure: &gatewayv1.GatewayInfrastructure{
						ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
					},
				},
			},
			&gatewayv1.GatewayClass{
				ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
				Spec: gatewayv1.GatewayClassSpec{
					ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
				},
			},
			&types.GatewayConfig{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "nlb-config"}},
		}
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient: fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithObjects(gatewayObjects...).
				Build(),
			NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{err: errors.New("nlb failed")},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
		})
		modelImpl := mustUDPRouteModelImpl(t, model)
		err := modelImpl.deprovisionDetachedRoute(t.Context(), route)
		require.ErrorContains(t, err, "nlb failed")

		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), mock.Anything, mock.Anything).
			RunAndReturn(func(_ context.Context, key apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				switch typed := obj.(type) {
				case *gatewayv1.Gateway:
					assert.Equal(t, apitypes.NamespacedName{Namespace: "iot", Name: "edge"}, key)
					typed.Namespace = "iot"
					typed.Name = "edge"
					typed.Spec.GatewayClassName = "oke-nlb"
					typed.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
						ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
					}
				case *gatewayv1.GatewayClass:
					typed.Name = "oke-nlb"
					typed.Spec.ControllerName = gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName)
				case *types.GatewayConfig:
					typed.Namespace = "iot"
					typed.Name = "nlb-config"
				default:
					t.Fatalf("unexpected Get object type %T", obj)
				}
				return nil
			})
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			Return(errors.New("update failed"))
		model = newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_old": {Name: new("bs_old")}},
				},
			},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
		})
		modelImpl = mustUDPRouteModelImpl(t, model)
		err = modelImpl.deprovisionDetachedRoute(t.Context(), route)
		require.ErrorContains(t, err, "failed to update detached UDPRoute")
	})

	t.Run("deprovisionDetachedRoute returns gateway read errors", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation: "bs_old",
				},
			},
			Status: gatewayv1.UDPRouteStatus{
				RouteStatus: gatewayv1.RouteStatus{
					Parents: []gatewayv1.RouteParentStatus{{
						ParentRef:      gatewayv1.ParentReference{Name: "edge"},
						ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
					}},
				},
			},
		}
		for name, tc := range map[string]struct {
			setup func(*Mockk8sClient)
			err   string
		}{
			"gateway": {
				setup: func(mockClient *Mockk8sClient) {
					mockClient.EXPECT().
						Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "edge"}, mock.AnythingOfType("*v1.Gateway")).
						Return(errors.New("gateway failed"))
				},
				err: "failed to get Gateway",
			},
			"gateway class": {
				setup: func(mockClient *Mockk8sClient) {
					mockClient.EXPECT().
						Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "edge"}, mock.AnythingOfType("*v1.Gateway")).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							gateway := mustGateway(t, obj)
							gateway.Namespace = "iot"
							gateway.Name = "edge"
							gateway.Spec.GatewayClassName = "oke-nlb"
							return nil
						})
					mockClient.EXPECT().
						Get(t.Context(), apitypes.NamespacedName{Name: "oke-nlb"}, mock.AnythingOfType("*v1.GatewayClass")).
						Return(errors.New("gateway class failed"))
				},
				err: "failed to get GatewayClass",
			},
			"config": {
				setup: func(mockClient *Mockk8sClient) {
					mockClient.EXPECT().
						Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "edge"}, mock.AnythingOfType("*v1.Gateway")).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							gateway := mustGateway(t, obj)
							gateway.Namespace = "iot"
							gateway.Name = "edge"
							gateway.Spec.GatewayClassName = "oke-nlb"
							gateway.Spec.Infrastructure = &gatewayv1.GatewayInfrastructure{
								ParametersRef: &gatewayv1.LocalParametersReference{Name: "nlb-config"},
							}
							return nil
						})
					mockClient.EXPECT().
						Get(t.Context(), apitypes.NamespacedName{Name: "oke-nlb"}, mock.AnythingOfType("*v1.GatewayClass")).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							gatewayClass := mustGatewayClass(t, obj)
							gatewayClass.Name = "oke-nlb"
							gatewayClass.Spec.ControllerName = gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName)
							return nil
						})
					mockClient.EXPECT().
						Get(
							t.Context(),
							apitypes.NamespacedName{Namespace: "iot", Name: "nlb-config"},
							mock.AnythingOfType("*types.GatewayConfig"),
						).
						Return(errors.New("config failed"))
				},
				err: "failed to get GatewayConfig",
			},
		} {
			t.Run(name, func(t *testing.T) {
				mockClient := NewMockk8sClient(t)
				tc.setup(mockClient)
				model := newUDPRouteModel(udpRouteModelDeps{
					RootLogger:                diag.RootTestLogger(),
					K8sClient:                 mockClient,
					NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{returnNil: true},
					OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
				})
				modelImpl := mustUDPRouteModelImpl(t, model)

				err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

				require.ErrorContains(t, err, tc.err)
			})
		}
	})

	t.Run("deprovisionRoute promotes next eligible route", func(t *testing.T) {
		port := gatewayv1.PortNumber(5684)
		listener := gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: port}
		now := metav1.Now()
		currentRoute := &gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "iot",
				Name:              "coap-old",
				Finalizers:        []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
				DeletionTimestamp: &now,
			},
			Spec: gatewayv1.UDPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: "edge", SectionName: lo.ToPtr(gatewayv1.SectionName("coap"))},
					},
				},
			},
		}
		nextRoute := &gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap-new",
				Generation: 2,
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation: "5684",
				},
			},
			Spec: gatewayv1.UDPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: "edge", SectionName: lo.ToPtr(gatewayv1.SectionName("coap"))},
					},
				},
				Rules: []gatewayv1.UDPRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					}},
				}},
			},
		}

		objects := append(l4GatewayObjects(listener), currentRoute, nextRoute)
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(objects...).
			WithStatusSubresource(&gatewayv1.UDPRoute{}).
			Build()
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_coap": {Name: new("bs_coap")}},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
		})

		err := model.deprovisionRoute(t.Context(), resolvedUDPRouteDetails{
			udpRoute: *currentRoute,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
					Spec:       gatewayv1.GatewaySpec{Listeners: []gatewayv1.Listener{listener}},
				},
			},
			matchedRef:      gatewayv1.ParentReference{Name: "edge"},
			matchedListener: listener,
		})

		require.NoError(t, err)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		var updatedNext gatewayv1.UDPRoute
		require.NoError(t, k8sClient.Get(
			t.Context(),
			apitypes.NamespacedName{Namespace: "iot", Name: "coap-new"},
			&updatedNext,
		))
		assert.Contains(t, updatedNext.Finalizers, NetworkLoadBalancerUDPRouteProgrammedFinalizer)
		assert.Len(t, updatedNext.Status.Parents, 1)
	})

	t.Run("deprovisionRoute wraps list update and next route errors", func(t *testing.T) {
		listener := gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: 5684}
		currentRoute := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap-old",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
			},
		}
		details := resolvedUDPRouteDetails{
			udpRoute: currentRoute,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			},
			matchedListener: listener,
		}

		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1.UDPRouteList")).
			Return(errors.New("list failed"))
		model := newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		err := model.deprovisionRoute(t.Context(), details)
		require.ErrorContains(t, err, "failed to list UDPRoutes for listener failover")

		mockClient = NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1.UDPRouteList")).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1.UDPRouteList{
					Items: []gatewayv1.UDPRoute{currentRoute},
				}))
				return nil
			})
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.UDPRoute")).
			Return(errors.New("update failed"))
		model = newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{Id: new("nlb-id")},
			},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
		})
		err = model.deprovisionRoute(t.Context(), details)
		require.ErrorContains(t, err, "failed to remove finalizer from UDPRoute")

		port := gatewayv1.PortNumber(5684)
		now := metav1.Now()
		deletingRoute := currentRoute
		deletingRoute.DeletionTimestamp = &now
		nextRoute := &gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "coap-new"},
			Spec: gatewayv1.UDPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				},
				Rules: []gatewayv1.UDPRouteRule{
					{
						BackendRefs: []gatewayv1.BackendRef{
							{BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend"}},
						},
					},
				},
			},
		}
		objects := append(l4GatewayObjects(listener), &deletingRoute, nextRoute)
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(objects...).
			Build()
		model = newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_coap": {Name: new("bs_coap")}},
				},
			},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
		})
		details.udpRoute = deletingRoute
		details.matchedListener = gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: port}
		err = model.deprovisionRoute(t.Context(), details)
		require.ErrorContains(t, err, "failed to program next UDPRoute")
	})

	t.Run("programRoute returns update and wait errors", func(t *testing.T) {
		port := gatewayv1.PortNumber(5684)
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "iot",
				Name:      "coap",
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation: "5684",
				},
			},
			Spec: gatewayv1.UDPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				},
				Rules: []gatewayv1.UDPRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					}},
				}},
			},
		}
		listener := gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: port}
		objects := append(l4GatewayObjects(listener), &route)

		for name, deps := range map[string]struct {
			client  *stubNetworkLoadBalancerClient
			watcher *stubWorkRequestsWatcher
			err     string
		}{
			"update": {
				client: &stubNetworkLoadBalancerClient{updateBackendSetErr: errors.New("update failed")},
				err:    "failed to update Network Load Balancer backend set",
			},
			"missing work request": {
				client: &stubNetworkLoadBalancerClient{omitUpdateBackendSetWorkRequest: true},
				err:    "missing work request id",
			},
			"wait": {
				client: &stubNetworkLoadBalancerClient{
					updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
						OpcWorkRequestId: new("work-request"),
					},
				},
				watcher: &stubWorkRequestsWatcher{err: errors.New("wait failed")},
				err:     "failed waiting for backend set bs_coap update",
			},
		} {
			t.Run(name, func(t *testing.T) {
				k8sClient := fake.NewClientBuilder().
					WithScheme(newL4TestScheme(t)).
					WithRuntimeObjects(objects...).
					Build()
				watcher := deps.watcher
				if watcher == nil {
					watcher = &stubWorkRequestsWatcher{}
				}
				model := newUDPRouteModel(udpRouteModelDeps{
					RootLogger: diag.RootTestLogger(),
					K8sClient:  k8sClient,
					NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
						networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
							Id: new("nlb-id"),
							BackendSets: map[string]networkloadbalancer.BackendSet{
								"bs_coap": {Name: new("bs_coap")},
							},
						},
					},
					OciNetworkLoadBalancerAPI: deps.client,
					WorkRequestsWatcher:       watcher,
				})

				err := model.programRoute(t.Context(), resolvedUDPRouteDetails{
					udpRoute: route,
					gatewayDetails: resolvedGatewayDetails{
						gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
					},
					matchedListener: listener,
				})

				require.ErrorContains(t, err, deps.err)
			})
		}
	})

	t.Run("programRoute returns busy error for backend set update conflict", func(t *testing.T) {
		port := gatewayv1.PortNumber(5684)
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "iot",
				Name:      "coap",
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation: "5684",
				},
			},
			Spec: gatewayv1.UDPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				},
				Rules: []gatewayv1.UDPRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					}},
				}},
			},
		}
		listener := gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: port}
		objects := append(l4GatewayObjects(listener), &route)
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(objects...).
			Build()
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_coap": {Name: new("bs_coap")},
					},
				},
			},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{
				updateBackendSetErr: ociapi.NewRandomServiceError(
					ociapi.RandomServiceErrorWithStatusCode(http.StatusConflict),
					ociapi.RandomServiceErrorWithCode("InvalidStateTransition"),
					ociapi.RandomServiceErrorWithMessage(
						"Invalid State Transition of NLB lifeCycle state from Updating to Updating",
					),
				),
			},
			WorkRequestsWatcher: &stubWorkRequestsWatcher{},
		})

		err := model.programRoute(t.Context(), resolvedUDPRouteDetails{
			udpRoute: route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			},
			matchedListener: listener,
		})

		var busyErr *networkLoadBalancerBusyError
		require.ErrorAs(t, err, &busyErr)
	})

	t.Run("programRoute returns busy error when NLB is already updating", func(t *testing.T) {
		port := gatewayv1.PortNumber(5684)
		listener := gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: port}
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "iot",
				Name:      "coap",
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation: "5684",
				},
			},
		}
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:             new("nlb-id"),
					LifecycleState: networkloadbalancer.LifecycleStateUpdating,
					BackendSets:    map[string]networkloadbalancer.BackendSet{"bs_coap": {Name: new("bs_coap")}},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
		})
		modelImpl := mustUDPRouteModelImpl(t, model)

		err := modelImpl.updateBackendSet(t.Context(), resolvedUDPRouteDetails{
			udpRoute: route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			},
			matchedListener: listener,
		}, "bs_coap", nil)

		var busyErr *networkLoadBalancerBusyError
		require.ErrorAs(t, err, &busyErr)
		assert.Empty(t, nlbClient.updateBackendSetRequests)
	})

	t.Run("programRoute uses annotated TCP health check port", func(t *testing.T) {
		port := gatewayv1.PortNumber(5684)
		healthCheckPort := 9000
		listener := gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: port}
		route := &gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "iot",
				Name:      "coap",
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation: "9000",
				},
			},
			Spec: gatewayv1.UDPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				},
				Rules: []gatewayv1.UDPRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					}},
				}},
			},
		}
		objects := append(l4GatewayObjects(listener), route)
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(objects...).
			Build()
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_coap": {Name: new("bs_coap")}},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
		})

		err := model.programRoute(t.Context(), resolvedUDPRouteDetails{
			udpRoute: *route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			},
			matchedListener: listener,
		})

		require.NoError(t, err)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		assert.Equal(t, healthCheckPort,
			lo.FromPtr(nlbClient.updateBackendSetRequests[0].UpdateBackendSetDetails.HealthChecker.Port))
		assert.Equal(t, networkloadbalancer.HealthCheckProtocolsTcp,
			nlbClient.updateBackendSetRequests[0].UpdateBackendSetDetails.HealthChecker.Protocol)
	})

	t.Run("programRoute rejects missing and invalid health check port annotation", func(t *testing.T) {
		port := gatewayv1.PortNumber(5684)
		listener := gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: port}
		for name, annotations := range map[string]map[string]string{
			"missing": nil,
			"invalid": {
				NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation: "nope",
			},
		} {
			t.Run(name, func(t *testing.T) {
				route := gatewayv1.UDPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:   "iot",
						Name:        "coap",
						Annotations: annotations,
					},
				}
				nlbClient := &stubNetworkLoadBalancerClient{}
				model := newUDPRouteModel(udpRouteModelDeps{
					RootLogger: diag.RootTestLogger(),
					K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
					NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
						networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{Id: new("nlb-id")},
					},
					OciNetworkLoadBalancerAPI: nlbClient,
					WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
				})

				err := model.programRoute(t.Context(), resolvedUDPRouteDetails{
					udpRoute: route,
					gatewayDetails: resolvedGatewayDetails{
						gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
					},
					matchedListener: listener,
				})

				var statusErr udpRouteStatusError
				require.ErrorAs(t, err, &statusErr)
				assert.Equal(t, gatewayv1.RouteConditionAccepted, statusErr.conditionType)
				assert.Equal(t, gatewayv1.RouteReasonUnsupportedValue, statusErr.reason)
				assert.Contains(t, statusErr.message, NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation)
				assert.Empty(t, nlbClient.updateBackendSetRequests)
			})
		}
	})

	t.Run("programRoute clears backend set when listener rejects attached route", func(t *testing.T) {
		listener := gatewayv1.Listener{
			Name:     "coap",
			Protocol: gatewayv1.UDPProtocolType,
			Port:     5684,
			AllowedRoutes: &gatewayv1.AllowedRoutes{
				Namespaces: &gatewayv1.RouteNamespaces{
					From: lo.ToPtr(gatewayv1.NamespacesFromSame),
				},
			},
		}
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "other",
				Name:       "coap",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
			},
		}
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_coap": {Name: new("bs_coap")}},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
		})

		err := model.programRoute(t.Context(), resolvedUDPRouteDetails{
			udpRoute: route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			},
			matchedListener: listener,
		})

		var statusErr udpRouteStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, gatewayv1.RouteReasonNotAllowedByListeners, statusErr.reason)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		assert.False(t, lo.FromPtr(nlbClient.updateBackendSetRequests[0].UpdateBackendSetDetails.IsPreserveSource))
		assert.Empty(t, nlbClient.updateBackendSetRequests[0].UpdateBackendSetDetails.Backends)
	})

	t.Run("programRoute skips update when backend set is current", func(t *testing.T) {
		port := gatewayv1.PortNumber(5684)
		listener := gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: port}
		route := &gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "iot",
				Name:      "coap",
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteHealthCheckPortAnnotation: "5684",
				},
			},
			Spec: gatewayv1.UDPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				},
				Rules: []gatewayv1.UDPRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					}},
				}},
			},
		}
		objects := append(l4GatewayObjects(listener), route)
		k8sClient := fake.NewClientBuilder().
			WithScheme(newL4TestScheme(t)).
			WithRuntimeObjects(objects...).
			Build()
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_coap": {
							Name:             new("bs_coap"),
							IsPreserveSource: new(false),
							HealthChecker: &networkloadbalancer.HealthChecker{
								Protocol: networkloadbalancer.HealthCheckProtocolsTcp,
								Port:     new(5684),
							},
							Backends: []networkloadbalancer.Backend{{
								IpAddress: new("10.0.0.10"),
								Port:      new(5684),
								IsDrain:   new(false),
								Weight:    new(1),
							}},
						},
					},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
		})

		err := model.programRoute(t.Context(), resolvedUDPRouteDetails{
			udpRoute: *route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			},
			matchedListener: listener,
		})

		require.NoError(t, err)
		assert.Empty(t, nlbClient.updateBackendSetRequests)
	})

	t.Run("clearBackendSetByName skips missing load balancer and backend set", func(t *testing.T) {
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger:                diag.RootTestLogger(),
			NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{returnNil: true},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
		})
		modelImpl := mustUDPRouteModelImpl(t, model)
		err := modelImpl.clearBackendSetByName(t.Context(), resolvedGatewayDetails{}, "iot/coap", "bs_coap", nil)
		require.NoError(t, err)

		nlbClient := &stubNetworkLoadBalancerClient{}
		model = newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
		})
		modelImpl = mustUDPRouteModelImpl(t, model)
		err = modelImpl.clearBackendSetByName(t.Context(), resolvedGatewayDetails{}, "iot/coap", "bs_coap", nil)
		require.NoError(t, err)
		assert.Empty(t, nlbClient.updateBackendSetRequests)

		nlbClient = &stubNetworkLoadBalancerClient{
			updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
				OpcWorkRequestId: new("work-request"),
			},
		}
		model = newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_coap": {Name: new("bs_coap")},
					},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{err: errors.New("wait failed")},
		})
		modelImpl = mustUDPRouteModelImpl(t, model)
		err = modelImpl.clearBackendSetByName(t.Context(), resolvedGatewayDetails{}, "iot/coap", "bs_coap", nil)
		require.ErrorContains(t, err, "failed waiting for backend set bs_coap clear")

		model = newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_coap": {Name: new("bs_coap")}},
				},
			},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{omitUpdateBackendSetWorkRequest: true},
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
		})
		modelImpl = mustUDPRouteModelImpl(t, model)
		err = modelImpl.clearBackendSetByName(t.Context(), resolvedGatewayDetails{}, "iot/coap", "bs_coap", nil)
		require.ErrorContains(t, err, "missing work request id")

		model = newUDPRouteModel(udpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_coap": {Name: new("bs_coap")}},
				},
			},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{updateBackendSetErr: errors.New("update failed")},
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
		})
		modelImpl = mustUDPRouteModelImpl(t, model)
		err = modelImpl.clearBackendSetByName(t.Context(), resolvedGatewayDetails{}, "iot/coap", "bs_coap", nil)
		require.ErrorContains(t, err, "failed to clear Network Load Balancer backend set bs_coap")
	})

	t.Run("clearStaleBackendSets keeps desired backend set", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "iot",
				Name:      "coap",
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation: "bs_coap",
				},
			},
		}
		model := newUDPRouteModel(udpRouteModelDeps{
			RootLogger:                diag.RootTestLogger(),
			NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{returnNil: true},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
		})
		modelImpl := mustUDPRouteModelImpl(t, model)
		err := modelImpl.clearStaleBackendSets(t.Context(), resolvedUDPRouteDetails{
			udpRoute: route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					Spec: gatewayv1.GatewaySpec{Listeners: []gatewayv1.Listener{
						{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: 5684},
					}},
				},
			},
		})
		require.NoError(t, err)
	})

	t.Run("deprovisionDetachedRoute skips unresolved gateway references", func(t *testing.T) {
		route := gatewayv1.UDPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "coap",
				Finalizers: []string{NetworkLoadBalancerUDPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerUDPRouteProgrammedBackendSetsAnnotation: "bs_coap",
				},
			},
			Status: gatewayv1.UDPRouteStatus{
				RouteStatus: gatewayv1.RouteStatus{
					Parents: []gatewayv1.RouteParentStatus{{
						ParentRef:      gatewayv1.ParentReference{Name: "edge"},
						ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
					}},
				},
			},
		}
		for name, objects := range map[string][]client.Object{
			"gateway missing": {},
			"class missing": {
				&gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
					Spec:       gatewayv1.GatewaySpec{GatewayClassName: "oke-nlb"},
				},
			},
			"wrong controller": {
				&gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
					Spec:       gatewayv1.GatewaySpec{GatewayClassName: "oke-nlb"},
				},
				&gatewayv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
					Spec: gatewayv1.GatewayClassSpec{
						ControllerName: gatewayv1.GatewayController("example.com/other"),
					},
				},
			},
			"missing infra": {
				&gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
					Spec:       gatewayv1.GatewaySpec{GatewayClassName: "oke-nlb"},
				},
				&gatewayv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{Name: "oke-nlb"},
					Spec: gatewayv1.GatewayClassSpec{
						ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
					},
				},
			},
		} {
			t.Run(name, func(t *testing.T) {
				model := newUDPRouteModel(udpRouteModelDeps{
					RootLogger: diag.RootTestLogger(),
					K8sClient: fake.NewClientBuilder().
						WithScheme(newL4TestScheme(t)).
						WithObjects(objects...).
						Build(),
					NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{returnNil: true},
					OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
				})
				modelImpl := mustUDPRouteModelImpl(t, model)

				err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

				require.NoError(t, err)
			})
		}
	})
}
