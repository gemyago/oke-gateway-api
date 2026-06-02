package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
	"github.com/gemyago/oke-gateway-api/internal/types"
)

type stubNetworkLoadBalancerGatewayModel struct {
	networkLoadBalancer networkloadbalancer.NetworkLoadBalancer
	returnNil           bool
	err                 error
}

func (s stubNetworkLoadBalancerGatewayModel) resolveReconcileRequest(
	context.Context,
	reconcile.Request,
	*resolvedGatewayDetails,
) (bool, error) {
	panic("not implemented")
}

func (s stubNetworkLoadBalancerGatewayModel) ensureNetworkLoadBalancer(
	context.Context,
	*resolvedGatewayDetails,
) (*networkloadbalancer.NetworkLoadBalancer, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.returnNil {
		var missing *networkloadbalancer.NetworkLoadBalancer
		return missing, nil
	}
	return &s.networkLoadBalancer, nil
}

func (s stubNetworkLoadBalancerGatewayModel) getNetworkLoadBalancer(
	context.Context,
	*resolvedGatewayDetails,
) (*networkloadbalancer.NetworkLoadBalancer, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.returnNil {
		var missing *networkloadbalancer.NetworkLoadBalancer
		return missing, nil
	}
	return &s.networkLoadBalancer, nil
}

func (s stubNetworkLoadBalancerGatewayModel) programGateway(context.Context, *resolvedGatewayDetails) error {
	panic("not implemented")
}

func (s stubNetworkLoadBalancerGatewayModel) deprovisionGateway(context.Context, *resolvedGatewayDetails) error {
	panic("not implemented")
}

func (s stubNetworkLoadBalancerGatewayModel) isProgrammed(context.Context, *resolvedGatewayDetails) bool {
	panic("not implemented")
}

func (s stubNetworkLoadBalancerGatewayModel) setProgrammed(
	context.Context,
	*resolvedGatewayDetails,
	*networkloadbalancer.NetworkLoadBalancer,
) error {
	panic("not implemented")
}

func TestTCPRouteModel(t *testing.T) {
	t.Run("desired backend sets ignore non gateway parent refs", func(t *testing.T) {
		otherGroup := gatewayv1.Group("example.com")
		details := resolvedTCPRouteDetails{
			tcpRoute: gatewayv1alpha2.TCPRoute{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
				Spec: gatewayv1alpha2.TCPRouteSpec{
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
							{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935},
						},
					},
				},
			},
		}

		assert.Empty(t, desiredTCPRouteBackendSetNames(details))
	})

	t.Run("resolveRequest wraps Kubernetes read errors", func(t *testing.T) {
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
			Spec: gatewayv1alpha2.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
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
							apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"},
							mock.AnythingOfType("*v1alpha2.TCPRoute"),
						).
						Return(errors.New("route failed"))
				},
				err: "failed to get TCPRoute",
			},
			"gateway": {
				setup: func(mockClient *Mockk8sClient) {
					mockClient.EXPECT().
						Get(
							t.Context(),
							apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"},
							mock.AnythingOfType("*v1alpha2.TCPRoute"),
						).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							*(mustTCPRoute(t, obj)) = route
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
							apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"},
							mock.AnythingOfType("*v1alpha2.TCPRoute"),
						).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							*(mustTCPRoute(t, obj)) = route
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
							apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"},
							mock.AnythingOfType("*v1alpha2.TCPRoute"),
						).
						RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
							*(mustTCPRoute(t, obj)) = route
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
				model := newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})

				_, err := model.resolveRequest(t.Context(), reconcile.Request{
					NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"},
				})

				require.ErrorContains(t, err, tc.err)
			})
		}
	})

	t.Run("resolveRequest returns status update errors for unmatched listener", func(t *testing.T) {
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
			Spec: gatewayv1alpha2.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
			}},
		}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"}, mock.AnythingOfType("*v1alpha2.TCPRoute")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				*(mustTCPRoute(t, obj)) = route
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
					{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: 5684},
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
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			Return(errors.New("status failed"))
		model := newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})

		_, err := model.resolveRequest(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"},
		})

		require.ErrorContains(t, err, "failed to update TCPRoute rtmp status")
	})

	t.Run("resolveRequest removes finalizer from deleting route with no resolved parent", func(t *testing.T) {
		now := metav1.Now()
		otherGroup := gatewayv1.Group("example.com")
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "iot",
				Name:              "rtmp",
				DeletionTimestamp: &now,
				Finalizers:        []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation: "bs_rtmp",
				},
			},
			Spec: gatewayv1alpha2.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Group: &otherGroup, Name: "edge"}},
			}},
		}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"}, mock.AnythingOfType("*v1alpha2.TCPRoute")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				*(mustTCPRoute(t, obj)) = route
				return nil
			})
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				updated := mustTCPRoute(t, obj)
				assert.NotContains(t, updated.Finalizers, NetworkLoadBalancerTCPRouteProgrammedFinalizer)
				assert.NotContains(t, updated.Annotations, NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation)
				return nil
			})
		model := newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})

		resolved, err := model.resolveRequest(t.Context(), reconcile.Request{
			NamespacedName: apitypes.NamespacedName{Namespace: "iot", Name: "rtmp"},
		})

		require.NoError(t, err)
		assert.Empty(t, resolved)
	})

	t.Run("programRoute rejects route when listener is already owned", func(t *testing.T) {
		currentRoute := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "z-route",
				Generation: 1,
			},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{
							Name:        "edge",
							SectionName: lo.ToPtr(gatewayv1.SectionName("rtmp")),
						},
					},
				},
			},
		}
		otherRoute := currentRoute
		otherRoute.Name = "a-route"

		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRouteList")).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1alpha2.TCPRouteList{
					Items: []gatewayv1alpha2.TCPRoute{currentRoute, otherRoute},
				}))
				return nil
			})

		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})

		err := model.programRoute(t.Context(), resolvedTCPRouteDetails{
			tcpRoute: currentRoute,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "iot",
						Name:      "edge",
					},
				},
			},
			matchedListener: gatewayv1.Listener{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
				Port:     1935,
			},
		})

		var statusErr tcpRouteStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, gatewayv1.RouteReasonNotAllowedByListeners, statusErr.reason)
		assert.Equal(t, "listener rtmp already has an attached TCPRoute iot/a-route", statusErr.message)
	})

	t.Run("listener ownership helpers return list errors", func(t *testing.T) {
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRouteList")).
			Return(errors.New("list failed")).
			Twice()
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})
		modelImpl := mustTCPRouteModelImpl(t, model)
		details := resolvedTCPRouteDetails{}
		err := modelImpl.ensureExclusiveListenerOwner(t.Context(), details)
		require.ErrorContains(t, err, "failed to list TCPRoutes for listener ownership check")
		_, err = modelImpl.nextEligibleRouteForListener(t.Context(), details)
		require.ErrorContains(t, err, "failed to list TCPRoutes for listener failover")
	})

	t.Run("listener ownership ignores non matching parent refs", func(t *testing.T) {
		serviceKind := gatewayv1.Kind("Service")
		routes := gatewayv1alpha2.TCPRouteList{Items: []gatewayv1alpha2.TCPRoute{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "service-parent"},
				Spec: gatewayv1alpha2.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Kind: &serviceKind, Name: "backend"}},
				}},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "other-gateway"},
				Spec: gatewayv1alpha2.TCPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "other"}},
				}},
			},
		}}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRouteList")).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(routes))
				return nil
			})
		model := newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		modelImpl := mustTCPRouteModelImpl(t, model)
		err := modelImpl.ensureExclusiveListenerOwner(t.Context(), resolvedTCPRouteDetails{
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			},
			tcpRoute:        gatewayv1alpha2.TCPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"}},
			matchedListener: gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935},
		})
		require.NoError(t, err)
	})

	t.Run("deprovisionRoute clears backend set and removes finalizer when no successor exists", func(t *testing.T) {
		currentRoute := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				},
			},
		}

		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRouteList")).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1alpha2.TCPRouteList{
					Items: []gatewayv1alpha2.TCPRoute{currentRoute},
				}))
				return nil
			})
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				assert.NotContains(t, obj.GetFinalizers(), NetworkLoadBalancerTCPRouteProgrammedFinalizer)
				assert.NotContains(t, obj.GetAnnotations(), NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation)
				return nil
			})

		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
		})

		err := model.deprovisionRoute(t.Context(), resolvedTCPRouteDetails{
			tcpRoute: currentRoute,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "iot",
						Name:      "edge",
					},
				},
			},
			matchedListener: gatewayv1.Listener{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
				Port:     1935,
			},
		})

		require.NoError(t, err)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		request := nlbClient.updateBackendSetRequests[0]
		assert.Equal(t, "nlb-id", lo.FromPtr(request.NetworkLoadBalancerId))
		assert.Equal(t, "bs_rtmp", lo.FromPtr(request.BackendSetName))
		assert.False(t, lo.FromPtr(request.UpdateBackendSetDetails.IsPreserveSource))
		assert.Empty(t, request.UpdateBackendSetDetails.Backends)
	})

	t.Run("endpointBackendsForRoute rejects invalid and unavailable backends", func(t *testing.T) {
		port := gatewayv1.PortNumber(1935)
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
		})
		modelImpl := mustTCPRouteModelImpl(t, model)
		_, err := modelImpl.endpointBackendsForRoute(t.Context(), gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				Rules: []gatewayv1alpha2.TCPRouteRule{
					{
						BackendRefs: []gatewayv1.BackendRef{
							{BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend"}},
						},
					},
				},
			},
		})
		var statusErr tcpRouteStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, gatewayv1.RouteReasonInvalidKind, statusErr.reason)

		backendNamespace := gatewayv1.Namespace("other")
		_, err = modelImpl.endpointBackendsForRoute(t.Context(), gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				Rules: []gatewayv1alpha2.TCPRouteRule{{
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
		model = newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		modelImpl = mustTCPRouteModelImpl(t, model)
		_, err = modelImpl.endpointBackendsForRoute(t.Context(), gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				Rules: []gatewayv1alpha2.TCPRouteRule{{
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
		model = newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		modelImpl = mustTCPRouteModelImpl(t, model)
		_, err = modelImpl.endpointBackendsForRoute(t.Context(), gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				Rules: []gatewayv1alpha2.TCPRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					}},
				}},
			},
		})
		require.ErrorContains(t, err, "failed to get service")
	})

	t.Run("setProgrammed adds finalizer and backend set annotation", func(t *testing.T) {
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "rtmp",
				Generation: 1,
			},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: "edge", SectionName: lo.ToPtr(gatewayv1.SectionName("rtmp"))},
					},
				},
			},
		}

		mockClient := NewMockk8sClient(t)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			Return(nil)
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				assert.Contains(t, obj.GetFinalizers(), NetworkLoadBalancerTCPRouteProgrammedFinalizer)
				assert.Equal(
					t,
					"bs_rtmp",
					obj.GetAnnotations()[NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation],
				)
				return nil
			})

		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})

		err := model.setProgrammed(t.Context(), resolvedTCPRouteDetails{
			tcpRoute: route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
					Spec: gatewayv1.GatewaySpec{
						Listeners: []gatewayv1.Listener{
							{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935},
						},
					},
				},
			},
			matchedRef: gatewayv1.ParentReference{
				Name: "edge",
			},
			matchedListener: gatewayv1.Listener{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
				Port:     1935,
			},
		})

		require.NoError(t, err)
	})

	t.Run("setProgrammed updates existing parent status and wraps errors", func(t *testing.T) {
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "rtmp",
				Generation: 2,
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation: "bs_rtmp",
				},
			},
			Status: gatewayv1alpha2.TCPRouteStatus{
				RouteStatus: gatewayv1.RouteStatus{
					Parents: []gatewayv1.RouteParentStatus{{
						ParentRef:      gatewayv1.ParentReference{Name: "edge"},
						ControllerName: gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName),
					}},
				},
			},
		}
		details := resolvedTCPRouteDetails{
			tcpRoute: route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"},
					Spec: gatewayv1.GatewaySpec{Listeners: []gatewayv1.Listener{
						{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935},
					}},
				},
			},
			matchedRef:      gatewayv1.ParentReference{Name: "edge"},
			matchedListener: gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935},
		}

		mockClient := NewMockk8sClient(t)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.SubResourceUpdateOption) error {
				updated := mustTCPRoute(t, obj)
				require.Len(t, updated.Status.Parents, 1)
				assert.Len(t, updated.Status.Parents[0].Conditions, 2)
				return nil
			})
		model := newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		require.NoError(t, model.setProgrammed(t.Context(), details))

		mockClient = NewMockk8sClient(t)
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			Return(errors.New("update failed"))
		model = newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		details.tcpRoute.Finalizers = nil
		details.tcpRoute.Annotations = nil
		err := model.setProgrammed(t.Context(), details)
		require.ErrorContains(t, err, "failed to update TCPRoute iot/rtmp finalizer and annotations")

		mockClient = NewMockk8sClient(t)
		mockStatusWriter = k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			Return(errors.New("status failed"))
		model = newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		details.tcpRoute = route
		err = model.setProgrammed(t.Context(), details)
		require.ErrorContains(t, err, "failed to update TCPRoute rtmp status")
	})

	t.Run("deprovisionDetachedRoute clears annotated backend set and removes finalizer", func(t *testing.T) {
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation: "bs_old",
				},
			},
			Status: gatewayv1alpha2.TCPRouteStatus{
				RouteStatus: gatewayv1.RouteStatus{
					Parents: []gatewayv1.RouteParentStatus{
						{
							ParentRef: gatewayv1.ParentReference{
								Name: "edge",
							},
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
						ParametersRef: &gatewayv1.LocalParametersReference{
							Name: "nlb-config",
						},
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
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				assert.NotContains(t, obj.GetFinalizers(), NetworkLoadBalancerTCPRouteProgrammedFinalizer)
				assert.NotContains(t, obj.GetAnnotations(), NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation)
				return nil
			})

		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_old": {
							Name: new("bs_old"),
							HealthChecker: &networkloadbalancer.HealthChecker{
								Protocol: networkloadbalancer.HealthCheckProtocolsTcp,
								Port:     new(1935),
							},
						},
					},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
		})

		modelImpl := mustTCPRouteModelImpl(t, model)
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
		assert.Equal(t, 1935, lo.FromPtr(request.UpdateBackendSetDetails.HealthChecker.Port))
	})

	t.Run("deprovisionDetachedRoute removes finalizer when no backend sets are annotated", func(t *testing.T) {
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			},
		}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.UpdateOption) error {
				assert.NotContains(t, obj.GetFinalizers(), NetworkLoadBalancerTCPRouteProgrammedFinalizer)
				return nil
			})
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})
		modelImpl := mustTCPRouteModelImpl(t, model)

		err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

		require.NoError(t, err)
	})

	t.Run("deprovisionDetachedRoute returns finalizer update error "+
		"when no backend sets are annotated", func(t *testing.T) {
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			},
		}
		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			Return(errors.New("update failed"))
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
		})
		modelImpl := mustTCPRouteModelImpl(t, model)

		err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

		require.ErrorContains(t, err, "failed to remove finalizer from detached TCPRoute")
	})

	t.Run("deprovisionDetachedRoute returns cleanup and update errors", func(t *testing.T) {
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation: "bs_old",
				},
			},
			Status: gatewayv1alpha2.TCPRouteStatus{
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
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient: fake.NewClientBuilder().
				WithScheme(newL4TestScheme(t)).
				WithObjects(gatewayObjects...).
				Build(),
			NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{err: errors.New("nlb failed")},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
		})
		modelImpl := mustTCPRouteModelImpl(t, model)
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
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			Return(errors.New("update failed"))
		model = newTCPRouteModel(tcpRouteModelDeps{
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
		modelImpl = mustTCPRouteModelImpl(t, model)
		err = modelImpl.deprovisionDetachedRoute(t.Context(), route)
		require.ErrorContains(t, err, "failed to update detached TCPRoute")
	})

	t.Run("deprovisionDetachedRoute returns gateway read errors", func(t *testing.T) {
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation: "bs_old",
				},
			},
			Status: gatewayv1alpha2.TCPRouteStatus{
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
				model := newTCPRouteModel(tcpRouteModelDeps{
					RootLogger:                diag.RootTestLogger(),
					K8sClient:                 mockClient,
					NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{returnNil: true},
					OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
				})
				modelImpl := mustTCPRouteModelImpl(t, model)

				err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

				require.ErrorContains(t, err, tc.err)
			})
		}
	})

	t.Run("deprovisionRoute promotes next eligible route", func(t *testing.T) {
		port := gatewayv1.PortNumber(1935)
		listener := gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: port}
		now := metav1.Now()
		currentRoute := &gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:         "iot",
				Name:              "rtmp-old",
				Finalizers:        []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
				DeletionTimestamp: &now,
			},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: "edge", SectionName: lo.ToPtr(gatewayv1.SectionName("rtmp"))},
					},
				},
			},
		}
		nextRoute := &gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp-new", Generation: 2},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{
						{Name: "edge", SectionName: lo.ToPtr(gatewayv1.SectionName("rtmp"))},
					},
				},
				Rules: []gatewayv1alpha2.TCPRouteRule{{
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
			WithStatusSubresource(&gatewayv1alpha2.TCPRoute{}).
			Build()
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_rtmp": {Name: new("bs_rtmp")}},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
		})

		err := model.deprovisionRoute(t.Context(), resolvedTCPRouteDetails{
			tcpRoute: *currentRoute,
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
		var updatedNext gatewayv1alpha2.TCPRoute
		require.NoError(t, k8sClient.Get(
			t.Context(),
			apitypes.NamespacedName{Namespace: "iot", Name: "rtmp-new"},
			&updatedNext,
		))
		assert.Contains(t, updatedNext.Finalizers, NetworkLoadBalancerTCPRouteProgrammedFinalizer)
		assert.Len(t, updatedNext.Status.Parents, 1)
	})

	t.Run("deprovisionRoute wraps list update and next route errors", func(t *testing.T) {
		listener := gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935}
		currentRoute := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "rtmp-old",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			},
		}
		details := resolvedTCPRouteDetails{
			tcpRoute: currentRoute,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			},
			matchedListener: listener,
		}

		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRouteList")).
			Return(errors.New("list failed"))
		model := newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
		err := model.deprovisionRoute(t.Context(), details)
		require.ErrorContains(t, err, "failed to list TCPRoutes for listener failover")

		mockClient = NewMockk8sClient(t)
		mockClient.EXPECT().
			List(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRouteList")).
			RunAndReturn(func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				reflect.ValueOf(list).Elem().Set(reflect.ValueOf(gatewayv1alpha2.TCPRouteList{
					Items: []gatewayv1alpha2.TCPRoute{currentRoute},
				}))
				return nil
			})
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1alpha2.TCPRoute")).
			Return(errors.New("update failed"))
		model = newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  mockClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{Id: new("nlb-id")},
			},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
		})
		err = model.deprovisionRoute(t.Context(), details)
		require.ErrorContains(t, err, "failed to remove finalizer from TCPRoute")

		port := gatewayv1.PortNumber(1935)
		now := metav1.Now()
		deletingRoute := currentRoute
		deletingRoute.DeletionTimestamp = &now
		nextRoute := &gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp-new"},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				},
				Rules: []gatewayv1alpha2.TCPRouteRule{
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
		model = newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_rtmp": {Name: new("bs_rtmp")}},
				},
			},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
		})
		details.tcpRoute = deletingRoute
		details.matchedListener = gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: port}
		err = model.deprovisionRoute(t.Context(), details)
		require.ErrorContains(t, err, "failed to program next TCPRoute")
	})

	t.Run("programRoute returns update and wait errors", func(t *testing.T) {
		port := gatewayv1.PortNumber(1935)
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				},
				Rules: []gatewayv1alpha2.TCPRouteRule{{
					BackendRefs: []gatewayv1.BackendRef{{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend", Port: &port},
					}},
				}},
			},
		}
		listener := gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: port}
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
			"wait": {
				client: &stubNetworkLoadBalancerClient{
					updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
						OpcWorkRequestId: new("work-request"),
					},
				},
				watcher: &stubWorkRequestsWatcher{err: errors.New("wait failed")},
				err:     "failed waiting for backend set bs_rtmp update",
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
				model := newTCPRouteModel(tcpRouteModelDeps{
					RootLogger: diag.RootTestLogger(),
					K8sClient:  k8sClient,
					NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
						networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
							Id: new("nlb-id"),
							BackendSets: map[string]networkloadbalancer.BackendSet{
								"bs_rtmp": {Name: new("bs_rtmp")},
							},
						},
					},
					OciNetworkLoadBalancerAPI: deps.client,
					WorkRequestsWatcher:       watcher,
				})

				err := model.programRoute(t.Context(), resolvedTCPRouteDetails{
					tcpRoute: route,
					gatewayDetails: resolvedGatewayDetails{
						gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
					},
					matchedListener: listener,
				})

				require.ErrorContains(t, err, deps.err)
			})
		}
	})

	t.Run("programRoute clears backend set when listener rejects attached route", func(t *testing.T) {
		listener := gatewayv1.Listener{
			Name:     "rtmp",
			Protocol: gatewayv1.TCPProtocolType,
			Port:     1935,
			AllowedRoutes: &gatewayv1.AllowedRoutes{
				Namespaces: &gatewayv1.RouteNamespaces{
					From: lo.ToPtr(gatewayv1.NamespacesFromSame),
				},
			},
		}
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "other",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			},
		}
		nlbClient := &stubNetworkLoadBalancerClient{}
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_rtmp": {Name: new("bs_rtmp")}},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
		})

		err := model.programRoute(t.Context(), resolvedTCPRouteDetails{
			tcpRoute: route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			},
			matchedListener: listener,
		})

		var statusErr tcpRouteStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, gatewayv1.RouteReasonNotAllowedByListeners, statusErr.reason)
		require.Len(t, nlbClient.updateBackendSetRequests, 1)
		assert.False(t, lo.FromPtr(nlbClient.updateBackendSetRequests[0].UpdateBackendSetDetails.IsPreserveSource))
		assert.Empty(t, nlbClient.updateBackendSetRequests[0].UpdateBackendSetDetails.Backends)
	})

	t.Run("programRoute skips update when backend set is current", func(t *testing.T) {
		port := gatewayv1.PortNumber(1935)
		listener := gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: port}
		route := &gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "rtmp"},
			Spec: gatewayv1alpha2.TCPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				},
				Rules: []gatewayv1alpha2.TCPRouteRule{{
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
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			K8sClient:  k8sClient,
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_rtmp": {
							Name:             new("bs_rtmp"),
							IsPreserveSource: new(false),
							Backends: []networkloadbalancer.Backend{{
								IpAddress: new("10.0.0.10"),
								Port:      new(1935),
								IsDrain:   new(false),
								Weight:    new(1),
							}},
						},
					},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
		})

		err := model.programRoute(t.Context(), resolvedTCPRouteDetails{
			tcpRoute: *route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "edge"}},
			},
			matchedListener: listener,
		})

		require.NoError(t, err)
		assert.Empty(t, nlbClient.updateBackendSetRequests)
	})

	t.Run("clearBackendSetByName skips missing load balancer and backend set", func(t *testing.T) {
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger:                diag.RootTestLogger(),
			NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{returnNil: true},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
		})
		modelImpl := mustTCPRouteModelImpl(t, model)
		err := modelImpl.clearBackendSetByName(t.Context(), resolvedGatewayDetails{}, "iot/rtmp", "bs_rtmp", nil)
		require.NoError(t, err)

		nlbClient := &stubNetworkLoadBalancerClient{}
		model = newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
		})
		modelImpl = mustTCPRouteModelImpl(t, model)
		err = modelImpl.clearBackendSetByName(t.Context(), resolvedGatewayDetails{}, "iot/rtmp", "bs_rtmp", nil)
		require.NoError(t, err)
		assert.Empty(t, nlbClient.updateBackendSetRequests)

		nlbClient = &stubNetworkLoadBalancerClient{
			updateBackendSetResponse: networkloadbalancer.UpdateBackendSetResponse{
				OpcWorkRequestId: new("work-request"),
			},
		}
		model = newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id: new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{
						"bs_rtmp": {Name: new("bs_rtmp")},
					},
				},
			},
			OciNetworkLoadBalancerAPI: nlbClient,
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{err: errors.New("wait failed")},
		})
		modelImpl = mustTCPRouteModelImpl(t, model)
		err = modelImpl.clearBackendSetByName(t.Context(), resolvedGatewayDetails{}, "iot/rtmp", "bs_rtmp", nil)
		require.ErrorContains(t, err, "failed waiting for backend set bs_rtmp clear")

		model = newTCPRouteModel(tcpRouteModelDeps{
			RootLogger: diag.RootTestLogger(),
			NetworkLoadBalancerModel: stubNetworkLoadBalancerGatewayModel{
				networkLoadBalancer: networkloadbalancer.NetworkLoadBalancer{
					Id:          new("nlb-id"),
					BackendSets: map[string]networkloadbalancer.BackendSet{"bs_rtmp": {Name: new("bs_rtmp")}},
				},
			},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{updateBackendSetErr: errors.New("update failed")},
			WorkRequestsWatcher:       &stubWorkRequestsWatcher{},
		})
		modelImpl = mustTCPRouteModelImpl(t, model)
		err = modelImpl.clearBackendSetByName(t.Context(), resolvedGatewayDetails{}, "iot/rtmp", "bs_rtmp", nil)
		require.ErrorContains(t, err, "failed to clear Network Load Balancer backend set bs_rtmp")
	})

	t.Run("clearStaleBackendSets keeps desired backend set", func(t *testing.T) {
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "iot",
				Name:      "rtmp",
				Annotations: map[string]string{
					NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation: "bs_rtmp",
				},
			},
		}
		model := newTCPRouteModel(tcpRouteModelDeps{
			RootLogger:                diag.RootTestLogger(),
			NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{returnNil: true},
			OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
		})
		modelImpl := mustTCPRouteModelImpl(t, model)
		err := modelImpl.clearStaleBackendSets(t.Context(), resolvedTCPRouteDetails{
			tcpRoute: route,
			gatewayDetails: resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{
					Spec: gatewayv1.GatewaySpec{Listeners: []gatewayv1.Listener{
						{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935},
					}},
				},
			},
		})
		require.NoError(t, err)
	})

	t.Run("deprovisionDetachedRoute skips unresolved gateway references", func(t *testing.T) {
		route := gatewayv1alpha2.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "iot",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
				Annotations: map[string]string{
					NetworkLoadBalancerTCPRouteProgrammedBackendSetsAnnotation: "bs_rtmp",
				},
			},
			Status: gatewayv1alpha2.TCPRouteStatus{
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
				model := newTCPRouteModel(tcpRouteModelDeps{
					RootLogger: diag.RootTestLogger(),
					K8sClient: fake.NewClientBuilder().
						WithScheme(newL4TestScheme(t)).
						WithObjects(objects...).
						Build(),
					NetworkLoadBalancerModel:  stubNetworkLoadBalancerGatewayModel{returnNil: true},
					OciNetworkLoadBalancerAPI: &stubNetworkLoadBalancerClient{},
				})
				modelImpl := mustTCPRouteModelImpl(t, model)

				err := modelImpl.deprovisionDetachedRoute(t.Context(), route)

				require.NoError(t, err)
			})
		}
	})
}
