package e2ek8s

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
)

func TestNewClient(t *testing.T) {
	t.Parallel()

	t.Run("allows empty kubeconfig path and falls back to default loading", func(t *testing.T) {
		t.Parallel()

		var gotConfig config.KubernetesConfig
		cfg := config.KubernetesConfig{
			KubeconfigPath: "",
			Context:        "oke-live",
		}

		client, err := NewClient(cfg, &ClientFactoryOptions{
			buildConfig: func(kubeConfig config.KubernetesConfig) (*rest.Config, error) {
				gotConfig = kubeConfig
				return &rest.Config{}, nil
			},
			newClient: func(_ *rest.Config, options ctrlclient.Options) (*RuntimeClient, error) {
				return &RuntimeClient{Scheme: options.Scheme}, nil
			},
			newScheme: NewScheme,
		})
		require.NoError(t, err)
		require.NotNil(t, client)
		assert.Equal(t, cfg, gotConfig)
	})

	t.Run("wraps kubeconfig build errors with context", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		cfg := config.KubernetesConfig{
			KubeconfigPath: "/tmp/kubeconfig",
			Context:        "oke-live",
		}

		_, err := NewClient(cfg, &ClientFactoryOptions{
			buildConfig: func(kubeConfig config.KubernetesConfig) (*rest.Config, error) {
				assert.Equal(t, cfg, kubeConfig)
				return nil, wantErr
			},
			newClient: newControllerRuntimeClient,
			newScheme: NewScheme,
		})
		require.Error(t, err)
		require.ErrorIs(t, err, wantErr)
		assert.Contains(t, err.Error(), "build Kubernetes REST config")
		assert.Contains(t, err.Error(), cfg.KubeconfigPath)
		assert.Contains(t, err.Error(), cfg.Context)
	})
}

func TestBuildRESTConfig(t *testing.T) {
	t.Parallel()

	t.Run("overrides current context from config", func(t *testing.T) {
		t.Parallel()

		kubeconfigPath := filepath.Join(t.TempDir(), "config")
		kubeconfig := clientcmdapi.Config{
			Clusters: map[string]*clientcmdapi.Cluster{
				"cluster-a": {Server: "https://127.0.0.1:6443"},
				"cluster-b": {Server: "https://192.0.2.42:6443"},
			},
			AuthInfos: map[string]*clientcmdapi.AuthInfo{
				"user-a": {},
				"user-b": {},
			},
			Contexts: map[string]*clientcmdapi.Context{
				"ctx-a": {Cluster: "cluster-a", AuthInfo: "user-a"},
				"ctx-b": {Cluster: "cluster-b", AuthInfo: "user-b"},
			},
			CurrentContext: "ctx-a",
		}
		require.NoError(t, clientcmd.WriteToFile(kubeconfig, kubeconfigPath))

		restConfig, err := buildRESTConfig(config.KubernetesConfig{
			KubeconfigPath: kubeconfigPath,
			Context:        "ctx-b",
		})
		require.NoError(t, err)
		assert.Equal(t, "https://192.0.2.42:6443", restConfig.Host)
	})

	t.Run("fails when the requested context is missing", func(t *testing.T) {
		t.Parallel()

		kubeconfigPath := filepath.Join(t.TempDir(), "config")
		kubeconfig := clientcmdapi.Config{
			Clusters: map[string]*clientcmdapi.Cluster{
				"cluster-a": {Server: "https://127.0.0.1:6443"},
			},
			AuthInfos: map[string]*clientcmdapi.AuthInfo{
				"user-a": {},
			},
			Contexts: map[string]*clientcmdapi.Context{
				"ctx-a": {Cluster: "cluster-a", AuthInfo: "user-a"},
			},
			CurrentContext: "ctx-a",
		}
		require.NoError(t, clientcmd.WriteToFile(kubeconfig, kubeconfigPath))

		_, err := buildRESTConfig(config.KubernetesConfig{
			KubeconfigPath: kubeconfigPath,
			Context:        "missing-context",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing-context")
	})
}

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

	t.Run("waits for namespace deletion", func(t *testing.T) {
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		err := WaitForNamespacesDeleted(
			t.Context(),
			kubeClient,
			[]string{"oke-gw-e2e-12345", "oke-gw-e2e-67890"},
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

	t.Run("waits for endpoint slices to stop publishing ready addresses", func(t *testing.T) {
		scheme := makeTestScheme(t)
		ready := false
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

		slices, err := WaitForServiceEndpointsGone(
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
