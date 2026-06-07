package e2ek8s

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeleteNamespacesWithPrefix(t *testing.T) {
	t.Run("deletes only matching namespaces", func(t *testing.T) {
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "oke-gw-e2e-12345"}},
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "oke-gw-e2e-67890"}},
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shared-namespace"}},
			).
			Build()

		deleted, err := DeleteNamespacesWithPrefix(t.Context(), kubeClient, "oke-gw-e2e-")
		require.NoError(t, err)
		assert.Equal(t, []string{"oke-gw-e2e-12345", "oke-gw-e2e-67890"}, deleted)

		var namespaces corev1.NamespaceList
		require.NoError(t, kubeClient.List(t.Context(), &namespaces))
		require.Len(t, namespaces.Items, 1)
		assert.Equal(t, "shared-namespace", namespaces.Items[0].Name)
	})

	t.Run("rejects empty prefix", func(t *testing.T) {
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		_, err := DeleteNamespacesWithPrefix(t.Context(), kubeClient, " ")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")
	})
}

func TestNewGatewayConfig(t *testing.T) {
	resource := NewGatewayConfig(GatewayConfigOptions{
		Namespace:      "oke-gw-e2e-12345",
		Name:           "gateway-config",
		LoadBalancerID: "ocid1.loadbalancer.oc1..example",
		Labels:         map[string]string{"case": "smoke"},
		Annotations:    map[string]string{"note": "fixture"},
	})

	assert.Equal(t, DefaultGatewayConfigGroup, resource.GroupVersionKind().Group)
	assert.Equal(t, DefaultGatewayConfigVersion, resource.GroupVersionKind().Version)
	assert.Equal(t, DefaultGatewayConfigKind, resource.GroupVersionKind().Kind)
	assert.Equal(t, "oke-gw-e2e-12345", resource.GetNamespace())
	assert.Equal(t, "gateway-config", resource.GetName())
	assert.Equal(t, "smoke", resource.GetLabels()["case"])
	assert.Equal(t, "fixture", resource.GetAnnotations()["note"])

	loadBalancerID, found, err := unstructured.NestedString(resource.Object, "spec", "loadBalancerId")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "ocid1.loadbalancer.oc1..example", loadBalancerID)
}

func TestWaiters(t *testing.T) {
	waitOpts := &WaitOptions{PollInterval: time.Millisecond}

	t.Run("waits for gateway conditions", func(t *testing.T) {
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "oke-gateway",
					Namespace:  "oke-gw-e2e-12345",
					Generation: 3,
				},
				Status: gatewayv1.GatewayStatus{
					Conditions: []metav1.Condition{
						{
							Type:               string(gatewayv1.GatewayConditionAccepted),
							Status:             metav1.ConditionTrue,
							ObservedGeneration: 3,
						},
						{
							Type:               string(gatewayv1.GatewayConditionProgrammed),
							Status:             metav1.ConditionTrue,
							ObservedGeneration: 3,
						},
					},
				},
			}).
			Build()

		_, err := WaitForGatewayAccepted(
			t.Context(),
			kubeClient,
			"oke-gw-e2e-12345",
			"oke-gateway",
			waitOpts,
		)
		require.NoError(t, err)

		_, err = WaitForGatewayProgrammed(
			t.Context(),
			kubeClient,
			"oke-gw-e2e-12345",
			"oke-gateway",
			waitOpts,
		)
		require.NoError(t, err)
	})

	t.Run("waits for route parent conditions", func(t *testing.T) {
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "echo-route",
					Namespace:  "oke-gw-e2e-12345",
					Generation: 2,
				},
				Status: gatewayv1.HTTPRouteStatus{
					RouteStatus: gatewayv1.RouteStatus{
						Parents: []gatewayv1.RouteParentStatus{
							{
								ParentRef: gatewayv1.ParentReference{
									Name: gatewayv1.ObjectName("oke-gateway"),
								},
								ControllerName: DefaultGatewayControllerName,
								Conditions: []metav1.Condition{
									{
										Type:               string(gatewayv1.RouteConditionAccepted),
										Status:             metav1.ConditionTrue,
										ObservedGeneration: 2,
									},
									{
										Type:               string(gatewayv1.RouteConditionResolvedRefs),
										Status:             metav1.ConditionTrue,
										ObservedGeneration: 2,
									},
								},
							},
						},
					},
				},
			}).
			Build()

		_, err := WaitForHTTPRouteAccepted(
			t.Context(),
			kubeClient,
			"oke-gw-e2e-12345",
			"echo-route",
			"oke-gateway",
			waitOpts,
		)
		require.NoError(t, err)

		_, err = WaitForHTTPRouteResolvedRefs(
			t.Context(),
			kubeClient,
			"oke-gw-e2e-12345",
			"echo-route",
			"oke-gateway",
			waitOpts,
		)
		require.NoError(t, err)
	})

	t.Run("waits for route deletion", func(t *testing.T) {
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		err := WaitForHTTPRouteDeleted(
			t.Context(),
			kubeClient,
			"oke-gw-e2e-12345",
			"echo-route",
			waitOpts,
		)
		require.NoError(t, err)
	})

	t.Run("waits for deployment availability", func(t *testing.T) {
		scheme := makeTestScheme(t)
		replicas := int32(1)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "echo",
					Namespace:  "oke-gw-e2e-12345",
					Generation: 1,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas,
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 1,
					ReadyReplicas:      1,
					AvailableReplicas:  1,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentAvailable,
							Status: corev1.ConditionTrue,
						},
					},
				},
			}).
			Build()

		_, err := WaitForDeploymentReady(
			t.Context(),
			kubeClient,
			"oke-gw-e2e-12345",
			"echo",
			waitOpts,
		)
		require.NoError(t, err)
	})

	t.Run("waits for endpoint slices", func(t *testing.T) {
		scheme := makeTestScheme(t)
		ready := true
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "echo-abcde",
					Namespace: "oke-gw-e2e-12345",
					Labels: map[string]string{
						discoveryv1.LabelServiceName: "echo",
					},
				},
				AddressType: discoveryv1.AddressTypeIPv4,
				Endpoints: []discoveryv1.Endpoint{
					{
						Addresses: []string{"10.0.0.12"},
						Conditions: discoveryv1.EndpointConditions{
							Ready: &ready,
						},
					},
				},
			}).
			Build()

		slices, err := WaitForServiceEndpointsReady(
			t.Context(),
			kubeClient,
			"oke-gw-e2e-12345",
			"echo",
			waitOpts,
		)
		require.NoError(t, err)
		require.Len(t, slices, 1)
		assert.Equal(t, "echo-abcde", slices[0].Name)
	})
}

func makeTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme, err := NewScheme()
	require.NoError(t, err)

	return scheme
}
