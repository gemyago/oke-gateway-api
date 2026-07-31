package app

import (
	"math/rand/v2"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestServicePortForBackendRef(t *testing.T) {
	fake := faker.New()

	t.Run("returns referenced service port", func(t *testing.T) {
		referencedPort := rand.Int32N(65534) + 1
		otherPort := referencedPort%65535 + 1
		expected := corev1.ServicePort{
			Name:       "referenced-" + fake.Lorem().Word(),
			Port:       referencedPort,
			TargetPort: intstr.FromInt32(rand.Int32N(65534) + 1),
		}
		service := corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{
				Name:       "other-" + fake.Lorem().Word(),
				Port:       otherPort,
				TargetPort: intstr.FromInt32(rand.Int32N(65534) + 1),
			},
			expected,
		}}}

		actual, err := servicePortForBackendRef(service, gatewayv1.BackendRef{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: gatewayv1.ObjectName("backend-" + fake.Lorem().Word()),
				Port: new(referencedPort),
			},
		})

		require.NoError(t, err)
		require.NotNil(t, actual)
		assert.Equal(t, expected, *actual)
	})

	t.Run("rejects backend ref without port", func(t *testing.T) {
		_, err := servicePortForBackendRef(corev1.Service{}, gatewayv1.BackendRef{
			BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: gatewayv1.ObjectName("backend-" + fake.Lorem().Word()),
			},
		})

		require.ErrorContains(t, err, "missing port")
	})

	t.Run("rejects port absent from service", func(t *testing.T) {
		servicePort := rand.Int32N(65534) + 1
		backendPort := servicePort%65535 + 1
		_, err := servicePortForBackendRef(
			corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: servicePort}}}},
			gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{
				Name: gatewayv1.ObjectName("backend-" + fake.Lorem().Word()),
				Port: new(backendPort),
			}},
		)

		require.ErrorContains(t, err, "has no port")
	})
}

func TestEndpointPortForServicePort(t *testing.T) {
	fake := faker.New()

	t.Run("falls back to service port when target port is omitted", func(t *testing.T) {
		servicePort := rand.Int32N(65534) + 1

		actual, ok := endpointPortForServicePort(
			corev1.ServicePort{Port: servicePort},
			discoveryv1.EndpointSlice{},
		)

		assert.True(t, ok)
		assert.Equal(t, int(servicePort), actual)
	})

	t.Run("returns numeric target port", func(t *testing.T) {
		targetPort := rand.Int32N(65534) + 1

		actual, ok := endpointPortForServicePort(
			corev1.ServicePort{
				Port:       targetPort%65535 + 1,
				TargetPort: intstr.FromInt32(targetPort),
			},
			discoveryv1.EndpointSlice{},
		)

		assert.True(t, ok)
		assert.Equal(t, int(targetPort), actual)
	})

	t.Run("returns named endpoint port", func(t *testing.T) {
		servicePortName := "service-" + fake.Lorem().Word()
		endpointPort := rand.Int32N(65534) + 1

		actual, ok := endpointPortForServicePort(
			corev1.ServicePort{
				Name:       servicePortName,
				Port:       rand.Int32N(65534) + 1,
				TargetPort: intstr.FromString("target-" + fake.Lorem().Word()),
			},
			discoveryv1.EndpointSlice{Ports: []discoveryv1.EndpointPort{{
				Name: &servicePortName,
				Port: &endpointPort,
			}}},
		)

		assert.True(t, ok)
		assert.Equal(t, int(endpointPort), actual)
	})

	t.Run("rejects endpoint slice without matching named port", func(t *testing.T) {
		servicePortName := "service-" + fake.Lorem().Word()
		otherPortName := "other-" + fake.Lorem().Word()
		endpointPort := rand.Int32N(65534) + 1

		_, ok := endpointPortForServicePort(
			corev1.ServicePort{
				Name:       servicePortName,
				Port:       rand.Int32N(65534) + 1,
				TargetPort: intstr.FromString("target-" + fake.Lorem().Word()),
			},
			discoveryv1.EndpointSlice{Ports: []discoveryv1.EndpointPort{{
				Name: &otherPortName,
				Port: &endpointPort,
			}}},
		)

		assert.False(t, ok)
	})

	t.Run("rejects named endpoint port without value", func(t *testing.T) {
		servicePortName := "service-" + fake.Lorem().Word()

		_, ok := endpointPortForServicePort(
			corev1.ServicePort{
				Name:       servicePortName,
				Port:       rand.Int32N(65534) + 1,
				TargetPort: intstr.FromString("target-" + fake.Lorem().Word()),
			},
			discoveryv1.EndpointSlice{Ports: []discoveryv1.EndpointPort{{
				Name: &servicePortName,
			}}},
		)

		assert.False(t, ok)
	})
}
