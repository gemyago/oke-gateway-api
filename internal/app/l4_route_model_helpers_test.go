package app

import (
	"context"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
)

func mustTCPRoute(t *testing.T, obj client.Object) *gatewayv1alpha2.TCPRoute {
	t.Helper()
	route, ok := obj.(*gatewayv1alpha2.TCPRoute)
	require.True(t, ok)
	return route
}

func mustUDPRoute(t *testing.T, obj client.Object) *gatewayv1alpha2.UDPRoute {
	t.Helper()
	route, ok := obj.(*gatewayv1alpha2.UDPRoute)
	require.True(t, ok)
	return route
}

func mustGateway(t *testing.T, obj client.Object) *gatewayv1.Gateway {
	t.Helper()
	gateway, ok := obj.(*gatewayv1.Gateway)
	require.True(t, ok)
	return gateway
}

func mustGatewayClass(t *testing.T, obj client.Object) *gatewayv1.GatewayClass {
	t.Helper()
	gatewayClass, ok := obj.(*gatewayv1.GatewayClass)
	require.True(t, ok)
	return gatewayClass
}

func mustService(t *testing.T, obj client.Object) *corev1.Service {
	t.Helper()
	service, ok := obj.(*corev1.Service)
	require.True(t, ok)
	return service
}

func mustNamespace(t *testing.T, obj client.Object) *corev1.Namespace {
	t.Helper()
	namespace, ok := obj.(*corev1.Namespace)
	require.True(t, ok)
	return namespace
}

func mustTCPRouteModelImpl(t *testing.T, model tcpRouteModel) *tcpRouteModelImpl {
	t.Helper()
	modelImpl, ok := model.(*tcpRouteModelImpl)
	require.True(t, ok)
	return modelImpl
}

func mustUDPRouteModelImpl(t *testing.T, model udpRouteModel) *udpRouteModelImpl {
	t.Helper()
	modelImpl, ok := model.(*udpRouteModelImpl)
	require.True(t, ok)
	return modelImpl
}

func mustNetworkLoadBalancerGatewayModelImpl(
	t *testing.T,
	model networkLoadBalancerGatewayModel,
) *networkLoadBalancerGatewayModelImpl {
	t.Helper()
	modelImpl, ok := model.(*networkLoadBalancerGatewayModelImpl)
	require.True(t, ok)
	return modelImpl
}

func TestL4RouteModelHelpers(t *testing.T) {
	t.Run("listener matches are ordered by creation timestamp then route key", func(t *testing.T) {
		listener := gatewayv1.Listener{
			Name:     "tls",
			Protocol: gatewayv1.TLSProtocolType,
		}
		gatewayNamespace := gatewayv1.Namespace("media")
		routes := []gatewayv1.TLSRoute{
			{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:         "media",
					Name:              "zzz-newer",
					CreationTimestamp: metav1.Unix(20, 0),
				},
				Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				}},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:         "media",
					Name:              "older",
					CreationTimestamp: metav1.Unix(10, 0),
				},
				Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: "edge"}},
				}},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:         "alpha",
					Name:              "tie",
					CreationTimestamp: metav1.Unix(20, 0),
				},
				Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Namespace: &gatewayNamespace, Name: "edge"}},
				}},
			},
		}

		matches := matchingL4RoutesForListener(
			routes,
			apitypes.NamespacedName{Namespace: "media", Name: "edge"},
			listener,
			"",
			tlsRouteKey,
			func(route gatewayv1.TLSRoute) string { return route.Namespace },
			func(route gatewayv1.TLSRoute) metav1.Time { return route.CreationTimestamp },
			func(route gatewayv1.TLSRoute) []gatewayv1.ParentReference { return route.Spec.ParentRefs },
			func(route gatewayv1.TLSRoute) bool { return route.DeletionTimestamp != nil },
			tlsRouteParentRefTarget,
			tlsRouteMatchesListener,
		)

		require.Len(t, matches, 3)
		assert.Equal(t, "media/older", matches[0].key)
		assert.Equal(t, "alpha/tie", matches[1].key)
		assert.Equal(t, "media/zzz-newer", matches[2].key)
	})

	t.Run("TCP helpers handle namespaces listeners backend equality and status errors", func(t *testing.T) {
		routeNamespace := gatewayv1.Namespace("media")
		port := gatewayv1.PortNumber(1935)
		backendRef := gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
			Namespace: &routeNamespace,
			Name:      "rtmp",
			Port:      &port,
		}}
		assert.Equal(t, apitypes.NamespacedName{Namespace: "media", Name: "rtmp"},
			tcpRouteBackendRefName(backendRef, "iot"))
		assert.Equal(t, apitypes.NamespacedName{Namespace: "media", Name: "edge"},
			tcpParentRefTarget(gatewayv1.ParentReference{Namespace: &routeNamespace, Name: "edge"}, "iot"))
		assert.False(t, tcpRouteMatchesListener(gatewayv1.ParentReference{}, gatewayv1.Listener{
			Protocol: gatewayv1.UDPProtocolType,
		}))
		assert.False(t, tcpRouteMatchesListener(
			gatewayv1.ParentReference{SectionName: lo.ToPtr(gatewayv1.SectionName("other"))},
			gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType},
		))
		assert.False(t, tcpRouteMatchesListener(
			gatewayv1.ParentReference{Port: lo.ToPtr(gatewayv1.PortNumber(80))},
			gatewayv1.Listener{Name: "rtmp", Protocol: gatewayv1.TCPProtocolType, Port: 1935},
		))
		assert.True(t, tcpBackendsEqual(
			[]networkloadbalancer.Backend{{
				IpAddress: new("10.0.0.10"),
				Port:      new(1935),
				Weight:    new(2),
				IsDrain:   new(false),
			}},
			[]networkloadbalancer.BackendDetails{{
				IpAddress: new("10.0.0.10"),
				Port:      new(1935),
				Weight:    new(2),
				IsDrain:   new(false),
			}},
		))
		assert.False(t, tcpBackendsEqual(nil, []networkloadbalancer.BackendDetails{{}}))
		assert.False(t, tcpBackendsEqual(
			[]networkloadbalancer.Backend{{IpAddress: new("10.0.0.10"), Port: new(1935)}},
			[]networkloadbalancer.BackendDetails{{IpAddress: new("10.0.0.11"), Port: new(1935)}},
		))
		assert.Equal(t, "rejected", tcpRouteStatusError{message: "rejected"}.Error())
		assert.Equal(t, "bad refs",
			newTCPRouteResolvedRefsStatusError(gatewayv1.RouteReasonInvalidKind, "bad refs").Error())
	})

	t.Run("UDP helpers handle namespaces listeners backend equality and status errors", func(t *testing.T) {
		routeNamespace := gatewayv1.Namespace("media")
		port := gatewayv1.PortNumber(5684)
		backendRef := gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
			Namespace: &routeNamespace,
			Name:      "coap",
			Port:      &port,
		}}
		assert.Equal(t, apitypes.NamespacedName{Namespace: "media", Name: "coap"},
			udpRouteBackendRefName(backendRef, "iot"))
		assert.Equal(t, apitypes.NamespacedName{Namespace: "media", Name: "edge"},
			udpParentRefTarget(gatewayv1.ParentReference{Namespace: &routeNamespace, Name: "edge"}, "iot"))
		assert.False(t, udpRouteMatchesListener(gatewayv1.ParentReference{}, gatewayv1.Listener{
			Protocol: gatewayv1.TCPProtocolType,
		}))
		assert.False(t, udpRouteMatchesListener(
			gatewayv1.ParentReference{SectionName: lo.ToPtr(gatewayv1.SectionName("other"))},
			gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType},
		))
		assert.False(t, udpRouteMatchesListener(
			gatewayv1.ParentReference{Port: lo.ToPtr(gatewayv1.PortNumber(80))},
			gatewayv1.Listener{Name: "coap", Protocol: gatewayv1.UDPProtocolType, Port: 5684},
		))
		assert.True(t, udpBackendsEqual(
			[]networkloadbalancer.Backend{{
				IpAddress: new("10.0.0.10"),
				Port:      new(5684),
				Weight:    new(2),
				IsDrain:   new(false),
			}},
			[]networkloadbalancer.BackendDetails{{
				IpAddress: new("10.0.0.10"),
				Port:      new(5684),
				Weight:    new(2),
				IsDrain:   new(false),
			}},
		))
		assert.False(t, udpBackendsEqual(nil, []networkloadbalancer.BackendDetails{{}}))
		assert.False(t, udpBackendsEqual(
			[]networkloadbalancer.Backend{{IpAddress: new("10.0.0.10"), Port: new(5684)}},
			[]networkloadbalancer.BackendDetails{{IpAddress: new("10.0.0.11"), Port: new(5684)}},
		))
		assert.Equal(t, "rejected", udpRouteStatusError{message: "rejected"}.Error())
		assert.Equal(t, "bad refs",
			newUDPRouteResolvedRefsStatusError(gatewayv1.RouteReasonInvalidKind, "bad refs").Error())
	})

	t.Run("TLS helpers handle listeners modes backend equality and status errors", func(t *testing.T) {
		routeNamespace := gatewayv1.Namespace("media")
		port := gatewayv1.PortNumber(443)
		backendRef := gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
			Namespace: &routeNamespace,
			Name:      "rtmps",
			Port:      &port,
		}}
		assert.Equal(t, apitypes.NamespacedName{Namespace: "media", Name: "rtmps"},
			tcpRouteBackendRefName(backendRef, "iot"))
		assert.Equal(t, apitypes.NamespacedName{Namespace: "media", Name: "edge"},
			tlsRouteParentRefTarget(gatewayv1.ParentReference{Namespace: &routeNamespace, Name: "edge"}, "iot"))
		assert.False(t, tlsRouteMatchesListener(gatewayv1.ParentReference{}, gatewayv1.Listener{
			Protocol: gatewayv1.TCPProtocolType,
		}))
		assert.False(t, tlsRouteMatchesListener(
			gatewayv1.ParentReference{SectionName: lo.ToPtr(gatewayv1.SectionName("other"))},
			gatewayv1.Listener{Name: "rtmps", Protocol: gatewayv1.TLSProtocolType},
		))
		assert.False(t, tlsRouteMatchesListener(
			gatewayv1.ParentReference{Port: lo.ToPtr(gatewayv1.PortNumber(8443))},
			gatewayv1.Listener{Name: "rtmps", Protocol: gatewayv1.TLSProtocolType, Port: 443},
		))
		assert.True(t, tlsRouteMatchesListener(
			gatewayv1.ParentReference{SectionName: lo.ToPtr(gatewayv1.SectionName("rtmps"))},
			gatewayv1.Listener{Name: "rtmps", Protocol: gatewayv1.TLSProtocolType, Port: 443},
		))
		mode, ok := tlsRouteMode(gatewayv1.Listener{TLS: &gatewayv1.ListenerTLSConfig{
			Mode: lo.ToPtr(gatewayv1.TLSModeTerminate),
		}})
		require.True(t, ok)
		assert.Equal(t, gatewayv1.TLSModeTerminate, mode)
		_, ok = tlsRouteMode(gatewayv1.Listener{})
		assert.False(t, ok)
		assert.Equal(t, "rejected", tlsRouteStatusError{message: "rejected"}.Error())
		assert.Equal(t, "bad refs",
			newTLSRouteResolvedRefsStatusError(gatewayv1.RouteReasonInvalidKind, "bad refs").Error())
		assert.True(t, loadBalancerBackendsEqual(
			[]loadbalancer.Backend{{
				IpAddress: new("10.0.0.10"),
				Port:      new(443),
				Weight:    new(2),
				Drain:     new(false),
			}},
			[]loadbalancer.BackendDetails{{
				IpAddress: new("10.0.0.10"),
				Port:      new(443),
				Weight:    new(2),
				Drain:     new(false),
			}},
		))
		assert.False(t, loadBalancerBackendsEqual(nil, []loadbalancer.BackendDetails{{}}))
	})
}

func TestL4RouteModelSetRejected(t *testing.T) {
	for name, tc := range map[string]struct {
		routeKind string
	}{
		"tcp": {routeKind: "tcp"},
		"udp": {routeKind: "udp"},
		"tls": {routeKind: "tls"},
	} {
		t.Run(name, func(t *testing.T) {
			mockClient := NewMockk8sClient(t)
			mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
			mockClient.EXPECT().Status().Return(mockStatusWriter)
			mockStatusWriter.EXPECT().
				Update(t.Context(), mock.Anything).
				RunAndReturn(func(_ context.Context, obj client.Object, _ ...client.SubResourceUpdateOption) error {
					switch route := obj.(type) {
					case *gatewayv1alpha2.TCPRoute:
						require.Len(t, route.Status.Parents, 1)
					case *gatewayv1alpha2.UDPRoute:
						require.Len(t, route.Status.Parents, 1)
					case *gatewayv1.TLSRoute:
						require.Len(t, route.Status.Parents, 1)
					default:
						t.Fatalf("unexpected status object %T", obj)
					}
					return nil
				})

			if tc.routeKind == "tcp" {
				model := newTCPRouteModel(tcpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
				err := model.setRejected(t.Context(), resolvedTCPRouteDetails{
					tcpRoute:   gatewayv1alpha2.TCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "rtmp", Generation: 1}},
					matchedRef: gatewayv1.ParentReference{Name: "edge"},
				}, newTCPRouteAcceptedStatusError(gatewayv1.RouteReasonNotAllowedByListeners, "blocked"))
				require.NoError(t, err)
				return
			}

			if tc.routeKind == "tls" {
				model := newTLSRouteModel(tlsRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
				err := model.setRejected(t.Context(), resolvedTLSRouteDetails{
					gatewayDetails: resolvedGatewayDetails{
						gatewayClass: gatewayv1.GatewayClass{Spec: gatewayv1.GatewayClassSpec{
							ControllerName: NetworkLoadBalancerControllerClassName,
						}},
					},
					tlsRoute:   gatewayv1.TLSRoute{ObjectMeta: metav1.ObjectMeta{Name: "rtmps", Generation: 1}},
					matchedRef: gatewayv1.ParentReference{Name: "edge"},
				}, newTLSRouteAcceptedStatusError(gatewayv1.RouteReasonNotAllowedByListeners, "blocked"))
				require.NoError(t, err)
				return
			}

			model := newUDPRouteModel(udpRouteModelDeps{RootLogger: diag.RootTestLogger(), K8sClient: mockClient})
			err := model.setRejected(t.Context(), resolvedUDPRouteDetails{
				udpRoute:   gatewayv1alpha2.UDPRoute{ObjectMeta: metav1.ObjectMeta{Name: "coap", Generation: 1}},
				matchedRef: gatewayv1.ParentReference{Name: "edge"},
			}, newUDPRouteAcceptedStatusError(gatewayv1.RouteReasonNotAllowedByListeners, "blocked"))
			require.NoError(t, err)
		})
	}
}
