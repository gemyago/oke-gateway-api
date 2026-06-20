package app

import (
	"context"
	"errors"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestL4RoutePolicy(t *testing.T) {
	t.Run("l4RouteKindAllowed applies protocol defaults and explicit kinds", func(t *testing.T) {
		assert.True(t, l4RouteKindAllowed(gatewayv1.Listener{Protocol: gatewayv1.TCPProtocolType}, "TCPRoute"))
		assert.True(t, l4RouteKindAllowed(gatewayv1.Listener{Protocol: gatewayv1.UDPProtocolType}, "UDPRoute"))
		assert.False(t, l4RouteKindAllowed(gatewayv1.Listener{Protocol: gatewayv1.HTTPProtocolType}, "TCPRoute"))
		assert.False(t, l4RouteKindAllowed(gatewayv1.Listener{Protocol: gatewayv1.ProtocolType("SCTP")}, "TCPRoute"))

		listener := gatewayv1.Listener{
			Protocol: gatewayv1.TCPProtocolType,
			AllowedRoutes: &gatewayv1.AllowedRoutes{
				Kinds: []gatewayv1.RouteGroupKind{
					{Kind: "UDPRoute"},
					{Group: lo.ToPtr(gatewayv1.Group("other.example.com")), Kind: "TCPRoute"},
				},
			},
		}
		assert.True(t, l4RouteKindAllowed(listener, "UDPRoute"))
		assert.False(t, l4RouteKindAllowed(listener, "TCPRoute"))
	})

	t.Run("l4RouteNamespaceAllowed handles same all none and selector policies", func(t *testing.T) {
		same := gatewayv1.NamespacesFromSame
		all := gatewayv1.NamespacesFromAll
		none := gatewayv1.NamespacesFromNone
		selector := gatewayv1.NamespacesFromSelector

		allowed, err := l4RouteNamespaceAllowed(t.Context(), NewMockk8sClient(t), "iot", "iot", gatewayv1.Listener{})
		require.NoError(t, err)
		assert.True(t, allowed)

		allowed, err = l4RouteNamespaceAllowed(t.Context(), NewMockk8sClient(t), "iot", "media", gatewayv1.Listener{
			AllowedRoutes: &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &same}},
		})
		require.NoError(t, err)
		assert.False(t, allowed)

		allowed, err = l4RouteNamespaceAllowed(t.Context(), NewMockk8sClient(t), "iot", "media", gatewayv1.Listener{
			AllowedRoutes: &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &all}},
		})
		require.NoError(t, err)
		assert.True(t, allowed)

		allowed, err = l4RouteNamespaceAllowed(t.Context(), NewMockk8sClient(t), "iot", "media", gatewayv1.Listener{
			AllowedRoutes: &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &none}},
		})
		require.NoError(t, err)
		assert.False(t, allowed)

		unknown := gatewayv1.FromNamespaces("Unknown")
		allowed, err = l4RouteNamespaceAllowed(t.Context(), NewMockk8sClient(t), "iot", "media", gatewayv1.Listener{
			AllowedRoutes: &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &unknown}},
		})
		require.NoError(t, err)
		assert.False(t, allowed)

		mockClient := NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), types.NamespacedName{Name: "media"}, mock.AnythingOfType("*v1.Namespace")).
			RunAndReturn(func(_ context.Context, _ types.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				namespace := mustNamespace(t, obj)
				namespace.Labels = map[string]string{"team": "edge"}
				return nil
			})
		allowed, err = l4RouteNamespaceAllowed(t.Context(), mockClient, "iot", "media", gatewayv1.Listener{
			AllowedRoutes: &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{
				From:     &selector,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "edge"}},
			}},
		})
		require.NoError(t, err)
		assert.True(t, allowed)

		allowed, err = l4RouteNamespaceAllowed(t.Context(), NewMockk8sClient(t), "iot", "media", gatewayv1.Listener{
			AllowedRoutes: &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: &selector}},
		})
		require.NoError(t, err)
		assert.False(t, allowed)

		allowed, err = l4RouteNamespaceAllowed(t.Context(), NewMockk8sClient(t), "iot", "media", gatewayv1.Listener{
			AllowedRoutes: &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{
				From:     &selector,
				Selector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Operator: "bad"}}},
			}},
		})
		require.Error(t, err)
		assert.False(t, allowed)

		mockClient = NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), types.NamespacedName{Name: "media"}, mock.AnythingOfType("*v1.Namespace")).
			Return(errors.New("namespace failed"))
		allowed, err = l4RouteNamespaceAllowed(t.Context(), mockClient, "iot", "media", gatewayv1.Listener{
			AllowedRoutes: &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{
				From:     &selector,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "edge"}},
			}},
		})
		require.ErrorContains(t, err, "failed to get route namespace media")
		assert.False(t, allowed)

		mockClient = NewMockk8sClient(t)
		mockClient.EXPECT().
			Get(t.Context(), types.NamespacedName{Name: "media"}, mock.AnythingOfType("*v1.Namespace")).
			RunAndReturn(func(_ context.Context, _ types.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				namespace := mustNamespace(t, obj)
				namespace.Labels = map[string]string{"team": "platform"}
				return nil
			})
		allowed, err = l4RouteNamespaceAllowed(t.Context(), mockClient, "iot", "media", gatewayv1.Listener{
			AllowedRoutes: &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{
				From:     &selector,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "edge"}},
			}},
		})
		require.NoError(t, err)
		assert.False(t, allowed)
	})

	t.Run("l4ListenerAllowsRoute rejects disallowed kind before namespace lookup", func(t *testing.T) {
		allowed, err := l4ListenerAllowsRoute(
			t.Context(),
			NewMockk8sClient(t),
			"iot",
			"iot",
			gatewayv1.Listener{Protocol: gatewayv1.TCPProtocolType},
			"UDPRoute",
		)

		require.NoError(t, err)
		assert.False(t, allowed)
	})

	t.Run("validates backend refs and weights", func(t *testing.T) {
		require.NoError(t, l4ValidateServiceBackendRef(gatewayv1.BackendRef{}))
		require.Error(t, l4ValidateServiceBackendRef(gatewayv1.BackendRef{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Group: lo.ToPtr(gatewayv1.Group("apps")),
				Kind:  lo.ToPtr(gatewayv1.Kind("Deployment")),
				Name:  "backend",
			},
		}))
		assert.Equal(t, 1, l4BackendRefWeight(gatewayv1.BackendRef{}))
		assert.Equal(t, 5, l4BackendRefWeight(gatewayv1.BackendRef{Weight: new(int32(5))}))
	})

	t.Run("resolves service backend ports to endpoint target ports", func(t *testing.T) {
		service := corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Name: "rtmp", Port: 1935},
			{Name: "api", Port: 8443, TargetPort: intstr.FromInt(9443)},
			{Name: "named", Port: 9000, TargetPort: intstr.FromString("traffic")},
		}}}

		servicePort, err := l4ServicePortForBackendRef(service, gatewayv1.BackendRef{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: "backend",
				Port: lo.ToPtr(gatewayv1.PortNumber(1935)),
			},
		})
		require.NoError(t, err)
		port, ok := l4EndpointPortForServicePort(*servicePort, discoveryv1.EndpointSlice{})
		assert.True(t, ok)
		assert.Equal(t, 1935, port)

		servicePort, err = l4ServicePortForBackendRef(service, gatewayv1.BackendRef{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: "backend",
				Port: lo.ToPtr(gatewayv1.PortNumber(8443)),
			},
		})
		require.NoError(t, err)
		port, ok = l4EndpointPortForServicePort(*servicePort, discoveryv1.EndpointSlice{})
		assert.True(t, ok)
		assert.Equal(t, 9443, port)

		servicePort, err = l4ServicePortForBackendRef(service, gatewayv1.BackendRef{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: "backend",
				Port: lo.ToPtr(gatewayv1.PortNumber(9000)),
			},
		})
		require.NoError(t, err)
		port, ok = l4EndpointPortForServicePort(*servicePort, discoveryv1.EndpointSlice{
			Ports: []discoveryv1.EndpointPort{
				{Name: new("other"), Port: new(int32(1111))},
				{Name: new("named"), Port: nil},
				{Name: new("named"), Port: new(int32(10000))},
			},
		})
		assert.True(t, ok)
		assert.Equal(t, 10000, port)

		_, err = l4ServicePortForBackendRef(service, gatewayv1.BackendRef{
			BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend"},
		})
		require.ErrorContains(t, err, "missing port")
		_, err = l4ServicePortForBackendRef(service, gatewayv1.BackendRef{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: "backend",
				Port: lo.ToPtr(gatewayv1.PortNumber(1234)),
			},
		})
		require.ErrorContains(t, err, "has no port")
		_, ok = l4EndpointPortForServicePort(*servicePort, discoveryv1.EndpointSlice{})
		assert.False(t, ok)
	})

	t.Run("parentRefTargetsGateway applies Gateway API defaults", func(t *testing.T) {
		assert.True(t, parentRefTargetsGateway(gatewayv1.ParentReference{Name: "edge"}))
		assert.False(t, parentRefTargetsGateway(gatewayv1.ParentReference{
			Group: lo.ToPtr(gatewayv1.Group("")),
			Kind:  lo.ToPtr(gatewayv1.Kind(serviceKind)),
			Name:  "edge",
		}))
	})
}
