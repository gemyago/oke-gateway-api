package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jaswdr/faker/v2"
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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
)

func mustTCPRoute(t *testing.T, obj client.Object) *gatewayv1.TCPRoute {
	t.Helper()
	route, ok := obj.(*gatewayv1.TCPRoute)
	require.True(t, ok)
	return route
}

func mustUDPRoute(t *testing.T, obj client.Object) *gatewayv1.UDPRoute {
	t.Helper()
	route, ok := obj.(*gatewayv1.UDPRoute)
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
			resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"}},
			},
			listener,
			"",
			tlsRouteKey,
			func(route gatewayv1.TLSRoute) string { return route.Namespace },
			func(route gatewayv1.TLSRoute) metav1.Time { return route.CreationTimestamp },
			func(route gatewayv1.TLSRoute) []gatewayv1.ParentReference { return route.Spec.ParentRefs },
			func(route gatewayv1.TLSRoute) bool { return route.DeletionTimestamp != nil },
			tlsRouteMatchesListener,
		)

		require.Len(t, matches, 3)
		assert.Equal(t, "media/older", matches[0].key)
		assert.Equal(t, "alpha/tie", matches[1].key)
		assert.Equal(t, "media/zzz-newer", matches[2].key)
	})

	t.Run("listener matches exclude current and deleting routes", func(t *testing.T) {
		fake := faker.New()
		listenerName := gatewayv1.SectionName("tls-" + fake.Lorem().Word())
		listener := gatewayv1.Listener{
			Name:     listenerName,
			Protocol: gatewayv1.TLSProtocolType,
		}
		deleteTime := metav1.Now()
		makeRoute := func(name string, deletionTimestamp *metav1.Time) gatewayv1.TLSRoute {
			return gatewayv1.TLSRoute{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:         "media",
					Name:              name,
					CreationTimestamp: metav1.Unix(10, 0),
					DeletionTimestamp: deletionTimestamp,
				},
				Spec: gatewayv1.TLSRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{
						Name:        "edge",
						SectionName: new(listenerName),
					}},
				}},
			}
		}
		deletedRouteName := "deleted-" + fake.Lorem().Word()
		excludedRouteName := "excluded-" + fake.Lorem().Word()
		matchedRouteName := "matched-" + fake.Lorem().Word()

		matches := matchingL4RoutesForListener(
			[]gatewayv1.TLSRoute{
				makeRoute(deletedRouteName, &deleteTime),
				makeRoute(excludedRouteName, nil),
				makeRoute(matchedRouteName, nil),
			},
			resolvedGatewayDetails{
				gateway: gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "edge"}},
			},
			listener,
			"media/"+excludedRouteName,
			tlsRouteKey,
			func(route gatewayv1.TLSRoute) string { return route.Namespace },
			func(route gatewayv1.TLSRoute) metav1.Time { return route.CreationTimestamp },
			func(route gatewayv1.TLSRoute) []gatewayv1.ParentReference { return route.Spec.ParentRefs },
			func(route gatewayv1.TLSRoute) bool { return route.DeletionTimestamp != nil },
			tlsRouteMatchesListener,
		)

		require.Len(t, matches, 1)
		assert.Equal(t, "media/"+matchedRouteName, matches[0].key)
	})

	t.Run("mergeNetworkLoadBalancerBackend aggregates only identical backend addresses", func(t *testing.T) {
		fake := faker.New()
		ipAddress := fake.Internet().Ipv4()
		firstPort := fake.IntBetween(1024, 30000)
		secondPort := firstPort + 1
		desired := map[string]networkloadbalancer.BackendDetails{}

		mergeNetworkLoadBalancerBackend(desired, networkloadbalancer.BackendDetails{
			IpAddress: new(ipAddress),
			Port:      new(firstPort),
			Weight:    new(3),
			IsDrain:   new(true),
		})
		mergeNetworkLoadBalancerBackend(desired, networkloadbalancer.BackendDetails{
			IpAddress: new(ipAddress),
			Port:      new(firstPort),
			Weight:    new(5),
			IsDrain:   new(false),
		})
		mergeNetworkLoadBalancerBackend(desired, networkloadbalancer.BackendDetails{
			IpAddress: new(ipAddress),
			Port:      new(secondPort),
			Weight:    new(7),
			IsDrain:   new(true),
		})

		firstKey := fmt.Sprintf("%s:%d", ipAddress, firstPort)
		secondKey := fmt.Sprintf("%s:%d", ipAddress, secondPort)
		assert.Equal(t, firstKey, networkLoadBalancerBackendKey(desired[firstKey]))
		assert.Equal(t, secondKey, networkLoadBalancerBackendKey(desired[secondKey]))
		assert.Equal(t, firstKey, lo.FromPtr(desired[firstKey].Name))
		assert.Equal(t, secondKey, lo.FromPtr(desired[secondKey].Name))
		assert.Equal(t, 8, lo.FromPtr(desired[firstKey].Weight))
		assert.False(t, lo.FromPtr(desired[firstKey].IsDrain))
		assert.Equal(t, 7, lo.FromPtr(desired[secondKey].Weight))
		assert.True(t, lo.FromPtr(desired[secondKey].IsDrain))
		assert.Len(t, desired, 2)
	})

	t.Run("TCP helpers handle namespaces listeners backend equality and status errors", func(t *testing.T) {
		fake := faker.New()
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
		firstIP := fake.Internet().Ipv4()
		secondIP := fake.Internet().Ipv4()
		firstPort := fake.IntBetween(1024, 30000)
		secondPort := firstPort + 1
		assert.True(t, tcpBackendsEqual(
			[]networkloadbalancer.Backend{
				{IpAddress: new(firstIP), Port: new(firstPort), Weight: new(2), IsDrain: new(false)},
				{IpAddress: new(secondIP), Port: new(secondPort), Weight: new(3), IsDrain: new(true)},
			},
			[]networkloadbalancer.BackendDetails{
				{IpAddress: new(secondIP), Port: new(secondPort), Weight: new(3), IsDrain: new(true)},
				{IpAddress: new(firstIP), Port: new(firstPort), Weight: new(2), IsDrain: new(false)},
			},
		))
		assert.False(t, tcpBackendsEqual(
			[]networkloadbalancer.Backend{{IpAddress: new(firstIP), Port: new(firstPort)}},
			[]networkloadbalancer.BackendDetails{{IpAddress: new(firstIP), Port: new(secondPort)}},
		))
		assert.Equal(t, "rejected", tcpRouteStatusError{message: "rejected"}.Error())
		assert.Equal(t, "bad refs",
			newTCPRouteResolvedRefsStatusError(gatewayv1.RouteReasonInvalidKind, "bad refs").Error())
	})

	t.Run("UDP helpers handle namespaces listeners backend equality and status errors", func(t *testing.T) {
		fake := faker.New()
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
		firstIP := fake.Internet().Ipv4()
		secondIP := fake.Internet().Ipv4()
		firstPort := fake.IntBetween(1024, 30000)
		secondPort := firstPort + 1
		assert.True(t, udpBackendsEqual(
			[]networkloadbalancer.Backend{
				{IpAddress: new(firstIP), Port: new(firstPort), Weight: new(2), IsDrain: new(false)},
				{IpAddress: new(secondIP), Port: new(secondPort), Weight: new(3), IsDrain: new(true)},
			},
			[]networkloadbalancer.BackendDetails{
				{IpAddress: new(secondIP), Port: new(secondPort), Weight: new(3), IsDrain: new(true)},
				{IpAddress: new(firstIP), Port: new(firstPort), Weight: new(2), IsDrain: new(false)},
			},
		))
		assert.False(t, udpBackendsEqual(
			[]networkloadbalancer.Backend{{IpAddress: new(firstIP), Port: new(firstPort)}},
			[]networkloadbalancer.BackendDetails{{IpAddress: new(firstIP), Port: new(secondPort)}},
		))
		assert.Equal(t, "rejected", udpRouteStatusError{message: "rejected"}.Error())
		assert.Equal(t, "bad refs",
			newUDPRouteResolvedRefsStatusError(gatewayv1.RouteReasonInvalidKind, "bad refs").Error())
	})

	t.Run("TLS helpers handle listeners modes backend equality and status errors", func(t *testing.T) {
		fake := faker.New()
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
		firstIP := fake.Internet().Ipv4()
		secondIP := fake.Internet().Ipv4()
		firstPort := fake.IntBetween(1024, 30000)
		secondPort := firstPort + 1
		assert.True(t, loadBalancerBackendsEqual(
			[]loadbalancer.Backend{
				{IpAddress: new(firstIP), Port: new(firstPort), Weight: new(2), Drain: new(false)},
				{IpAddress: new(secondIP), Port: new(secondPort), Weight: new(3), Drain: new(true)},
			},
			[]loadbalancer.BackendDetails{
				{IpAddress: new(secondIP), Port: new(secondPort), Weight: new(3), Drain: new(true)},
				{IpAddress: new(firstIP), Port: new(firstPort), Weight: new(2), Drain: new(false)},
			},
		))
		assert.False(t, loadBalancerBackendsEqual(
			[]loadbalancer.Backend{{IpAddress: new(firstIP), Port: new(firstPort)}},
			[]loadbalancer.BackendDetails{{IpAddress: new(firstIP), Port: new(secondPort)}},
		))
	})

	t.Run("programL4Route clears programmed backend set when listener rejects route", func(t *testing.T) {
		same := gatewayv1.NamespacesFromSame
		route := &gatewayv1.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "routes",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			},
		}
		k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build()
		acceptedErr := errors.New("route rejected")
		clearCalls := 0

		err := programL4Route(t.Context(), programL4RouteParams{
			k8sClient:         k8sClient,
			routeKind:         "TCPRoute",
			route:             route,
			listenerNamespace: "gateway",
			listener: gatewayv1.Listener{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{From: &same},
				},
			},
			finalizer: NetworkLoadBalancerTCPRouteProgrammedFinalizer,
			clearBackendSet: func() error {
				clearCalls++
				return nil
			},
			ensureExclusiveListenerOwner: func() error {
				t.Fatal("exclusive owner check should not run for rejected listener")
				return nil
			},
			clearStaleBackendSets: func() error {
				t.Fatal("stale backend set cleanup should not run for rejected listener")
				return nil
			},
			endpointBackendsForRoute: func() ([]networkloadbalancer.BackendDetails, error) {
				t.Fatal("backend resolution should not run for rejected listener")
				return nil, nil
			},
			acceptedStatusError: func(reason gatewayv1.RouteConditionReason, message string) error {
				assert.Equal(t, gatewayv1.RouteReasonNotAllowedByListeners, reason)
				assert.Contains(t, message, "listener rtmp does not allow TCPRoute routes/rtmp")
				return acceptedErr
			},
		})

		require.ErrorIs(t, err, acceptedErr)
		assert.Equal(t, 1, clearCalls)
	})

	t.Run("programL4Route clears programmed backend set after backend resolution status error", func(t *testing.T) {
		route := &gatewayv1.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "gateway",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			},
		}
		k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build()
		resolveErr := newTCPRouteResolvedRefsStatusError(gatewayv1.RouteReasonBackendNotFound, "backend missing")
		clearCalls := 0

		err := programL4Route(t.Context(), programL4RouteParams{
			k8sClient:         k8sClient,
			routeKind:         "TCPRoute",
			route:             route,
			listenerNamespace: "gateway",
			listener: gatewayv1.Listener{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
			},
			finalizer: NetworkLoadBalancerTCPRouteProgrammedFinalizer,
			clearBackendSet: func() error {
				clearCalls++
				return nil
			},
			ensureExclusiveListenerOwner: func() error {
				return nil
			},
			clearStaleBackendSets: func() error {
				return nil
			},
			endpointBackendsForRoute: func() ([]networkloadbalancer.BackendDetails, error) {
				return nil, resolveErr
			},
			backendResolutionStatusError: func(err error) bool {
				var statusErr tcpRouteStatusError
				return errors.As(err, &statusErr) &&
					statusErr.conditionType == gatewayv1.RouteConditionResolvedRefs
			},
			updateBackendSet: func(string, []networkloadbalancer.BackendDetails) error {
				t.Fatal("backend set update should not run after backend resolution error")
				return nil
			},
		})

		var statusErr tcpRouteStatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, gatewayv1.RouteConditionResolvedRefs, statusErr.conditionType)
		assert.Equal(t, 1, clearCalls)
	})

	t.Run("programL4Route wraps cleanup failure after listener rejection", func(t *testing.T) {
		same := gatewayv1.NamespacesFromSame
		route := &gatewayv1.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "routes",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			},
		}
		k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build()
		cleanupErr := errors.New("delete backend set failed")

		err := programL4Route(t.Context(), programL4RouteParams{
			k8sClient:         k8sClient,
			routeKind:         "TCPRoute",
			route:             route,
			listenerNamespace: "gateway",
			listener: gatewayv1.Listener{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{From: &same},
				},
			},
			finalizer: NetworkLoadBalancerTCPRouteProgrammedFinalizer,
			clearBackendSet: func() error {
				return cleanupErr
			},
		})

		require.ErrorIs(t, err, cleanupErr)
		require.ErrorContains(t, err, "failed to clear backend set after TCPRoute attachment was rejected")
	})

	t.Run("programL4Route wraps cleanup failure after backend resolution status error", func(t *testing.T) {
		route := &gatewayv1.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "gateway",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			},
		}
		k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build()
		resolveErr := newTCPRouteResolvedRefsStatusError(gatewayv1.RouteReasonBackendNotFound, "backend missing")
		cleanupErr := errors.New("delete backend set failed")

		err := programL4Route(t.Context(), programL4RouteParams{
			k8sClient:         k8sClient,
			routeKind:         "TCPRoute",
			route:             route,
			listenerNamespace: "gateway",
			listener: gatewayv1.Listener{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
			},
			finalizer: NetworkLoadBalancerTCPRouteProgrammedFinalizer,
			clearBackendSet: func() error {
				return cleanupErr
			},
			ensureExclusiveListenerOwner: func() error {
				return nil
			},
			clearStaleBackendSets: func() error {
				return nil
			},
			endpointBackendsForRoute: func() ([]networkloadbalancer.BackendDetails, error) {
				return nil, resolveErr
			},
			backendResolutionStatusError: func(err error) bool {
				var statusErr tcpRouteStatusError
				return errors.As(err, &statusErr) &&
					statusErr.conditionType == gatewayv1.RouteConditionResolvedRefs
			},
		})

		require.ErrorIs(t, err, cleanupErr)
		require.ErrorContains(t, err, "failed to clear backend set after TCPRoute backend resolution error")
	})

	t.Run("programL4Route clears stale backend sets before updating already programmed route", func(t *testing.T) {
		route := &gatewayv1.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "gateway",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			},
		}
		k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build()
		backends := []networkloadbalancer.BackendDetails{{
			IpAddress: new("10.0.3.20"),
			Port:      new(1935),
		}}
		events := make([]string, 0, 3)

		err := programL4Route(t.Context(), programL4RouteParams{
			k8sClient:         k8sClient,
			routeKind:         "TCPRoute",
			route:             route,
			listenerNamespace: "gateway",
			listener: gatewayv1.Listener{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
			},
			finalizer: NetworkLoadBalancerTCPRouteProgrammedFinalizer,
			clearBackendSet: func() error {
				t.Fatal("current backend set cleanup should not run for successful backend resolution")
				return nil
			},
			ensureExclusiveListenerOwner: func() error {
				events = append(events, "owner")
				return nil
			},
			clearStaleBackendSets: func() error {
				events = append(events, "clear-stale")
				return nil
			},
			endpointBackendsForRoute: func() ([]networkloadbalancer.BackendDetails, error) {
				events = append(events, "resolve")
				return backends, nil
			},
			backendResolutionStatusError: func(error) bool {
				return false
			},
			updateBackendSet: func(name string, gotBackends []networkloadbalancer.BackendDetails) error {
				events = append(events, "update")
				assert.Equal(t, "bs_rtmp", name)
				assert.Equal(t, backends, gotBackends)
				return nil
			},
		})

		require.NoError(t, err)
		assert.Equal(t, []string{"owner", "clear-stale", "resolve", "update"}, events)
	})

	t.Run("programL4Route returns listener namespace selector errors", func(t *testing.T) {
		fromSelector := gatewayv1.NamespacesFromSelector
		route := &gatewayv1.TCPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "routes", Name: "rtmp"}}
		k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build()

		err := programL4Route(t.Context(), programL4RouteParams{
			k8sClient:         k8sClient,
			routeKind:         "TCPRoute",
			route:             route,
			listenerNamespace: "gateway",
			listener: gatewayv1.Listener{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
				AllowedRoutes: &gatewayv1.AllowedRoutes{
					Namespaces: &gatewayv1.RouteNamespaces{
						From: &fromSelector,
						Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
							Key:      "team",
							Operator: metav1.LabelSelectorOperator("invalid"),
							Values:   []string{"iot"},
						}}},
					},
				},
			},
		})

		require.ErrorContains(t, err, "invalid allowedRoutes namespace selector")
	})

	t.Run("programL4Route returns stale backend set cleanup errors", func(t *testing.T) {
		route := &gatewayv1.TCPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "gateway",
				Name:       "rtmp",
				Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
			},
		}
		k8sClient := fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build()
		staleErr := errors.New("stale cleanup failed")

		err := programL4Route(t.Context(), programL4RouteParams{
			k8sClient:         k8sClient,
			routeKind:         "TCPRoute",
			route:             route,
			listenerNamespace: "gateway",
			listener: gatewayv1.Listener{
				Name:     "rtmp",
				Protocol: gatewayv1.TCPProtocolType,
			},
			finalizer: NetworkLoadBalancerTCPRouteProgrammedFinalizer,
			ensureExclusiveListenerOwner: func() error {
				return nil
			},
			clearStaleBackendSets: func() error {
				return staleErr
			},
		})

		require.ErrorIs(t, err, staleErr)
	})

	t.Run("deprovisionL4Route wraps next route programmed status errors", func(t *testing.T) {
		statusErr := errors.New("status failed")
		route := &gatewayv1.TCPRoute{ObjectMeta: metav1.ObjectMeta{
			Namespace:  "iot",
			Name:       "old-rtmp",
			Finalizers: []string{NetworkLoadBalancerTCPRouteProgrammedFinalizer},
		}}
		nextRoute := resolvedTCPRouteDetails{
			tcpRoute: gatewayv1.TCPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: "iot", Name: "new-rtmp"}},
		}

		err := deprovisionL4Route(t.Context(), deprovisionL4RouteParams[resolvedTCPRouteDetails]{
			k8sClient:     fake.NewClientBuilder().WithScheme(newL4TestScheme(t)).Build(),
			routeKind:     "TCPRoute",
			routeToUpdate: route,
			finalizer:     NetworkLoadBalancerTCPRouteProgrammedFinalizer,
			nextRoute: func() (*resolvedTCPRouteDetails, error) {
				return &nextRoute, nil
			},
			programRoute: func(resolvedTCPRouteDetails) error {
				return nil
			},
			setProgrammed: func(resolvedTCPRouteDetails) error {
				return statusErr
			},
			routeObject: func(route resolvedTCPRouteDetails) client.Object {
				return &route.tcpRoute
			},
		})

		require.ErrorIs(t, err, statusErr)
		require.ErrorContains(t, err, "failed to set next TCPRoute iot/new-rtmp programmed status")
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
					case *gatewayv1.TCPRoute:
						require.Len(t, route.Status.Parents, 1)
					case *gatewayv1.UDPRoute:
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
					tcpRoute:   gatewayv1.TCPRoute{ObjectMeta: metav1.ObjectMeta{Name: "rtmp", Generation: 1}},
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
				udpRoute:   gatewayv1.UDPRoute{ObjectMeta: metav1.ObjectMeta{Name: "coap", Generation: 1}},
				matchedRef: gatewayv1.ParentReference{Name: "edge"},
			}, newUDPRouteAcceptedStatusError(gatewayv1.RouteReasonNotAllowedByListeners, "blocked"))
			require.NoError(t, err)
		})
	}
}
