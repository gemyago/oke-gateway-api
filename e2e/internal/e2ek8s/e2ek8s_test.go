package e2ek8s

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8swatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
)

func TestNewClient(t *testing.T) {
	t.Parallel()

	t.Run("allows empty kubeconfig path and falls back to default loading", func(t *testing.T) {
		t.Parallel()

		fakerGen := faker.New()
		var gotConfig config.KubernetesConfig
		cfg := config.KubernetesConfig{
			KubeconfigPath: "",
			Context:        "oke-live-" + fakerGen.UUID().V4(),
		}

		client, err := NewClient(cfg, &ClientFactoryOptions{
			buildConfig: func(kubeConfig config.KubernetesConfig) (*rest.Config, error) {
				gotConfig = kubeConfig
				return &rest.Config{}, nil
			},
			newClient: func(_ *rest.Config, options ctrlclient.Options) (*RuntimeClient, error) {
				client := fake.NewClientBuilder().WithScheme(options.Scheme).Build()
				return &RuntimeClient{
					WithWatch: client,
					Client:    client,
					Scheme:    options.Scheme,
				}, nil
			},
			newScheme: NewScheme,
		})
		require.NoError(t, err)
		require.NotNil(t, client)
		assert.Equal(t, cfg, gotConfig)
	})

	t.Run("wraps kubeconfig build errors with context", func(t *testing.T) {
		t.Parallel()

		fakerGen := faker.New()
		wantErr := errors.New("boom")
		cfg := config.KubernetesConfig{
			KubeconfigPath: "/tmp/kubeconfig",
			Context:        "oke-live-" + fakerGen.UUID().V4(),
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

		fakerGen := faker.New()
		clusterAName := "cluster-a-" + fakerGen.UUID().V4()
		clusterBName := "cluster-b-" + fakerGen.UUID().V4()
		userAName := "user-a-" + fakerGen.UUID().V4()
		userBName := "user-b-" + fakerGen.UUID().V4()
		contextAName := "ctx-a-" + fakerGen.UUID().V4()
		contextBName := "ctx-b-" + fakerGen.UUID().V4()
		kubeconfigPath := filepath.Join(t.TempDir(), "config")
		kubeconfig := clientcmdapi.Config{
			Clusters: map[string]*clientcmdapi.Cluster{
				clusterAName: {Server: "https://127.0.0.1:6443"},
				clusterBName: {Server: "https://192.0.2.42:6443"},
			},
			AuthInfos: map[string]*clientcmdapi.AuthInfo{
				userAName: {},
				userBName: {},
			},
			Contexts: map[string]*clientcmdapi.Context{
				contextAName: {Cluster: clusterAName, AuthInfo: userAName},
				contextBName: {Cluster: clusterBName, AuthInfo: userBName},
			},
			CurrentContext: contextAName,
		}
		require.NoError(t, clientcmd.WriteToFile(kubeconfig, kubeconfigPath))

		restConfig, err := buildRESTConfig(config.KubernetesConfig{
			KubeconfigPath: kubeconfigPath,
			Context:        contextBName,
		})
		require.NoError(t, err)
		assert.Equal(t, "https://192.0.2.42:6443", restConfig.Host)
	})

	t.Run("fails when the requested context is missing", func(t *testing.T) {
		t.Parallel()

		fakerGen := faker.New()
		clusterName := "cluster-" + fakerGen.UUID().V4()
		userName := "user-" + fakerGen.UUID().V4()
		contextName := "ctx-" + fakerGen.UUID().V4()
		missingContextName := "missing-context-" + fakerGen.UUID().V4()
		kubeconfigPath := filepath.Join(t.TempDir(), "config")
		kubeconfig := clientcmdapi.Config{
			Clusters: map[string]*clientcmdapi.Cluster{
				clusterName: {Server: "https://127.0.0.1:6443"},
			},
			AuthInfos: map[string]*clientcmdapi.AuthInfo{
				userName: {},
			},
			Contexts: map[string]*clientcmdapi.Context{
				contextName: {Cluster: clusterName, AuthInfo: userName},
			},
			CurrentContext: contextName,
		}
		require.NoError(t, clientcmd.WriteToFile(kubeconfig, kubeconfigPath))

		_, err := buildRESTConfig(config.KubernetesConfig{
			KubeconfigPath: kubeconfigPath,
			Context:        missingContextName,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), missingContextName)
	})
}

func TestDeleteNamespacesWithPrefix(t *testing.T) {
	t.Run("deletes only matching namespaces", func(t *testing.T) {
		fakerGen := faker.New()
		prefix := "oke-gw-e2e-" + fakerGen.UUID().V4() + "-"
		namespaceAName := prefix + "a"
		namespaceBName := prefix + "b"
		sharedNamespaceName := "shared-namespace-" + fakerGen.UUID().V4()
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceAName}},
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceBName}},
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sharedNamespaceName}},
			).
			Build()

		deleted, err := DeleteNamespacesWithPrefix(t.Context(), kubeClient, prefix)
		require.NoError(t, err)
		assert.Equal(t, []string{namespaceAName, namespaceBName}, deleted)

		var namespaces corev1.NamespaceList
		require.NoError(t, kubeClient.List(t.Context(), &namespaces))
		require.Len(t, namespaces.Items, 1)
		assert.Equal(t, sharedNamespaceName, namespaces.Items[0].Name)
	})

	t.Run("removes managed HTTPRoute finalizers in matching namespaces before deletion", func(t *testing.T) {
		fakerGen := faker.New()
		prefix := "oke-gw-e2e-" + fakerGen.UUID().V4() + "-"
		namespaceName := prefix + "routes"
		otherNamespaceName := "shared-namespace-" + fakerGen.UUID().V4()
		managedRouteName := "route-managed-" + fakerGen.UUID().V4()
		otherRouteName := "route-other-" + fakerGen.UUID().V4()
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}},
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: otherNamespaceName}},
				&gatewayv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Name:       managedRouteName,
						Namespace:  namespaceName,
						Finalizers: []string{httpRouteProgrammedFinalizer},
					},
				},
				&gatewayv1.HTTPRoute{
					ObjectMeta: metav1.ObjectMeta{
						Name:       otherRouteName,
						Namespace:  otherNamespaceName,
						Finalizers: []string{httpRouteProgrammedFinalizer},
					},
				},
			).
			Build()

		deleted, err := DeleteNamespacesWithPrefix(t.Context(), kubeClient, prefix)
		require.NoError(t, err)
		assert.Equal(t, []string{namespaceName}, deleted)

		var managedRoute gatewayv1.HTTPRoute
		require.NoError(
			t,
			kubeClient.Get(
				t.Context(),
				ctrlclient.ObjectKey{Namespace: namespaceName, Name: managedRouteName},
				&managedRoute,
			),
		)
		assert.NotContains(t, managedRoute.Finalizers, httpRouteProgrammedFinalizer)

		var otherRoute gatewayv1.HTTPRoute
		require.NoError(
			t,
			kubeClient.Get(
				t.Context(),
				ctrlclient.ObjectKey{Namespace: otherNamespaceName, Name: otherRouteName},
				&otherRoute,
			),
		)
		assert.Contains(t, otherRoute.Finalizers, httpRouteProgrammedFinalizer)
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
	fakerGen := faker.New()
	namespaceName := "oke-gw-e2e-" + fakerGen.UUID().V4()
	gatewayConfigName := "gateway-config-" + fakerGen.UUID().V4()
	expectedLoadBalancerID := "ocid1.loadbalancer.oc1..example-" + fakerGen.UUID().V4()
	labelValue := "case-" + fakerGen.UUID().V4()
	annotationValue := "note-" + fakerGen.UUID().V4()
	resource := NewGatewayConfig(GatewayConfigOptions{
		Namespace:      namespaceName,
		Name:           gatewayConfigName,
		LoadBalancerID: expectedLoadBalancerID,
		Labels:         map[string]string{"case": labelValue},
		Annotations:    map[string]string{"note": annotationValue},
	})

	assert.Equal(t, DefaultGatewayConfigGroup, resource.GroupVersionKind().Group)
	assert.Equal(t, DefaultGatewayConfigVersion, resource.GroupVersionKind().Version)
	assert.Equal(t, DefaultGatewayConfigKind, resource.GroupVersionKind().Kind)
	assert.Equal(t, namespaceName, resource.GetNamespace())
	assert.Equal(t, gatewayConfigName, resource.GetName())
	assert.Equal(t, labelValue, resource.GetLabels()["case"])
	assert.Equal(t, annotationValue, resource.GetAnnotations()["note"])

	actualLoadBalancerID, found, err := unstructured.NestedString(resource.Object, "spec", "loadBalancerId")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, expectedLoadBalancerID, actualLoadBalancerID)
}

func TestNewStaticHTTPDeployment(t *testing.T) {
	t.Parallel()

	fake := faker.New()
	namespace := "oke-gw-e2e-" + fake.UUID().V4()
	name := "backend-a-" + fake.UUID().V4()
	responseText := "backend-a-response-" + fake.UUID().V4()

	deployment := NewStaticHTTPDeployment(StaticHTTPDeploymentOptions{
		Namespace:    namespace,
		Name:         name,
		ResponseText: responseText,
	})

	require.NotNil(t, deployment.Spec.Replicas)
	assert.Equal(t, int32(1), *deployment.Spec.Replicas)
	assert.Equal(t, DefaultStaticHTTPImage, deployment.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, []string{"sh", "-ceu"}, deployment.Spec.Template.Spec.Containers[0].Command)
	assert.Equal(t, namespace, deployment.Namespace)
	assert.Equal(t, name, deployment.Name)
	assert.Equal(
		t,
		responseText,
		deployment.Spec.Template.Spec.Containers[0].Env[0].Value,
	)
	assert.Equal(
		t,
		int32(8080),
		deployment.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort,
	)
}

func TestNewHTTPRoute(t *testing.T) {
	t.Parallel()

	t.Run("builds route with default prefix path", func(t *testing.T) {
		t.Parallel()

		fake := faker.New()
		namespace := "oke-gw-e2e-" + fake.UUID().V4()
		routeName := "echo-route-" + fake.UUID().V4()
		gatewayName := "gateway-" + fake.UUID().V4()
		serviceName := "backend-a-" + fake.UUID().V4()
		hostname := gatewayv1.Hostname("route-a-" + fake.UUID().V4() + ".example.test")
		pathPrefix := "/echo-" + fake.UUID().V4()

		route := NewHTTPRoute(HTTPRouteOptions{
			Namespace:    namespace,
			Name:         routeName,
			GatewayName:  gatewayName,
			ServiceName:  serviceName,
			ServicePort:  8080,
			PathPrefix:   pathPrefix,
			Hostnames:    []gatewayv1.Hostname{hostname},
			ListenerName: DefaultHTTPListenerName,
		})

		require.Len(t, route.Spec.Hostnames, 1)
		assert.Equal(t, namespace, route.Namespace)
		assert.Equal(t, routeName, route.Name)
		assert.Equal(t, hostname, route.Spec.Hostnames[0])
		require.Len(t, route.Spec.Rules, 1)
		require.Len(t, route.Spec.Rules[0].Matches, 1)
		require.NotNil(t, route.Spec.Rules[0].Matches[0].Path)
		assert.Equal(t, pathPrefix, *route.Spec.Rules[0].Matches[0].Path.Value)
		assert.Empty(t, route.Spec.Rules[0].Matches[0].Headers)
	})

	t.Run("builds route with custom path and header matches", func(t *testing.T) {
		t.Parallel()

		fake := faker.New()
		namespace := "oke-gw-e2e-" + fake.UUID().V4()
		routeName := "echo-route-" + fake.UUID().V4()
		gatewayName := "gateway-" + fake.UUID().V4()
		serviceName := "backend-b-" + fake.UUID().V4()
		pathValue := "/exact-" + fake.UUID().V4()
		pathType := gatewayv1.PathMatchExact
		headerType := gatewayv1.HeaderMatchRegularExpression
		headerName := gatewayv1.HTTPHeaderName("X-Route-" + fake.UUID().V4())
		headerValue := "^prefix-" + fake.UUID().V4()

		route := NewHTTPRoute(HTTPRouteOptions{
			Namespace:   namespace,
			Name:        routeName,
			GatewayName: gatewayName,
			ServiceName: serviceName,
			ServicePort: 8080,
			PathMatch: &gatewayv1.HTTPPathMatch{
				Type:  &pathType,
				Value: &pathValue,
			},
			HeaderMatches: []gatewayv1.HTTPHeaderMatch{
				{
					Type:  &headerType,
					Name:  headerName,
					Value: headerValue,
				},
			},
			ListenerName: DefaultHTTPListenerName,
		})

		require.Len(t, route.Spec.Rules, 1)
		require.Len(t, route.Spec.Rules[0].Matches, 1)
		require.NotNil(t, route.Spec.Rules[0].Matches[0].Path)
		require.NotNil(t, route.Spec.Rules[0].Matches[0].Path.Type)
		require.NotNil(t, route.Spec.Rules[0].Matches[0].Path.Value)
		assert.Equal(t, pathType, *route.Spec.Rules[0].Matches[0].Path.Type)
		assert.Equal(t, pathValue, *route.Spec.Rules[0].Matches[0].Path.Value)
		require.Len(t, route.Spec.Rules[0].Matches[0].Headers, 1)
		assert.Equal(t, headerName, route.Spec.Rules[0].Matches[0].Headers[0].Name)
		assert.Equal(t, headerValue, route.Spec.Rules[0].Matches[0].Headers[0].Value)
		require.NotNil(t, route.Spec.Rules[0].Matches[0].Headers[0].Type)
		assert.Equal(t, headerType, *route.Spec.Rules[0].Matches[0].Headers[0].Type)
	})

	t.Run("builds route with header matches and no path match", func(t *testing.T) {
		t.Parallel()

		fake := faker.New()
		namespace := "oke-gw-e2e-" + fake.UUID().V4()
		routeName := "echo-route-" + fake.UUID().V4()
		gatewayName := "gateway-" + fake.UUID().V4()
		serviceName := "backend-c-" + fake.UUID().V4()
		headerType := gatewayv1.HeaderMatchExact
		headerName := gatewayv1.HTTPHeaderName("X-Route-" + fake.UUID().V4())
		headerValue := "exact-" + fake.UUID().V4()

		route := NewHTTPRoute(HTTPRouteOptions{
			Namespace:     namespace,
			Name:          routeName,
			GatewayName:   gatewayName,
			ServiceName:   serviceName,
			ServicePort:   8080,
			OmitPathMatch: true,
			HeaderMatches: []gatewayv1.HTTPHeaderMatch{
				{
					Type:  &headerType,
					Name:  headerName,
					Value: headerValue,
				},
			},
			ListenerName: DefaultHTTPListenerName,
		})

		require.Len(t, route.Spec.Rules, 1)
		require.Len(t, route.Spec.Rules[0].Matches, 1)
		assert.Nil(t, route.Spec.Rules[0].Matches[0].Path)
		require.Len(t, route.Spec.Rules[0].Matches[0].Headers, 1)
		assert.Equal(t, headerName, route.Spec.Rules[0].Matches[0].Headers[0].Name)
		assert.Equal(t, headerValue, route.Spec.Rules[0].Matches[0].Headers[0].Value)
	})
}

func TestWaiters(t *testing.T) {
	waitOpts := &WaitOptions{PollInterval: time.Millisecond}

	t.Run("waits for gateway conditions", func(t *testing.T) {
		fakerGen := faker.New()
		namespaceName := "oke-gw-e2e-" + fakerGen.UUID().V4()
		gatewayName := "oke-gateway-" + fakerGen.UUID().V4()
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:       gatewayName,
					Namespace:  namespaceName,
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
			namespaceName,
			gatewayName,
			waitOpts,
		)
		require.NoError(t, err)

		_, err = WaitForGatewayProgrammed(
			t.Context(),
			kubeClient,
			namespaceName,
			gatewayName,
			waitOpts,
		)
		require.NoError(t, err)
	})

	t.Run("waits for gateway conditions after watch updates", func(t *testing.T) {
		fakerGen := faker.New()
		key := ctrlclient.ObjectKey{
			Name:      "oke-gateway-" + fakerGen.UUID().V4(),
			Namespace: "oke-gw-e2e-" + fakerGen.UUID().V4(),
		}
		scheme := makeTestScheme(t)
		baseClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{
					Name:       key.Name,
					Namespace:  key.Namespace,
					Generation: 2,
				},
			}).
			Build()

		watchStarted := make(chan struct{})
		var watchStartedOnce sync.Once
		kubeClient := interceptor.NewClient(baseClient, interceptor.Funcs{
			Watch: func(
				ctx context.Context,
				client ctrlclient.WithWatch,
				list ctrlclient.ObjectList,
				opts ...ctrlclient.ListOption,
			) (k8swatch.Interface, error) {
				watchStartedOnce.Do(func() {
					close(watchStarted)
				})

				return client.Watch(ctx, list, opts...)
			},
		})

		updateErrCh := make(chan error, 1)
		go func() {
			<-watchStarted

			gateway := &gatewayv1.Gateway{}
			if err := baseClient.Get(t.Context(), key, gateway); err != nil {
				updateErrCh <- err
				return
			}

			gateway.Status.Conditions = []metav1.Condition{
				{
					Type:               string(gatewayv1.GatewayConditionAccepted),
					Status:             metav1.ConditionTrue,
					ObservedGeneration: gateway.Generation,
				},
			}

			updateErrCh <- baseClient.Update(t.Context(), gateway)
		}()

		_, err := WaitForGatewayAccepted(
			t.Context(),
			kubeClient,
			key.Namespace,
			key.Name,
			waitOpts,
		)
		require.NoError(t, err)
		require.NoError(t, <-updateErrCh)
	})

	t.Run("waits for route parent conditions", func(t *testing.T) {
		fakerGen := faker.New()
		namespaceName := "oke-gw-e2e-" + fakerGen.UUID().V4()
		routeName := "echo-route-" + fakerGen.UUID().V4()
		gatewayName := "oke-gateway-" + fakerGen.UUID().V4()
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:       routeName,
					Namespace:  namespaceName,
					Generation: 2,
				},
				Status: gatewayv1.HTTPRouteStatus{
					RouteStatus: gatewayv1.RouteStatus{
						Parents: []gatewayv1.RouteParentStatus{
							{
								ParentRef: gatewayv1.ParentReference{
									Name: gatewayv1.ObjectName(gatewayName),
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
			namespaceName,
			routeName,
			gatewayName,
			waitOpts,
		)
		require.NoError(t, err)

		_, err = WaitForHTTPRouteResolvedRefs(
			t.Context(),
			kubeClient,
			namespaceName,
			routeName,
			gatewayName,
			waitOpts,
		)
		require.NoError(t, err)
	})

	t.Run("waits for route deletion", func(t *testing.T) {
		fakerGen := faker.New()
		namespaceName := "oke-gw-e2e-" + fakerGen.UUID().V4()
		routeName := "echo-route-" + fakerGen.UUID().V4()
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		err := WaitForHTTPRouteDeleted(
			t.Context(),
			kubeClient,
			namespaceName,
			routeName,
			waitOpts,
		)
		require.NoError(t, err)
	})

	t.Run("waits for route deletion after watch updates", func(t *testing.T) {
		fakerGen := faker.New()
		key := ctrlclient.ObjectKey{
			Name:      "echo-route-" + fakerGen.UUID().V4(),
			Namespace: "oke-gw-e2e-" + fakerGen.UUID().V4(),
		}
		scheme := makeTestScheme(t)
		baseClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{
					Name:      key.Name,
					Namespace: key.Namespace,
				},
			}).
			Build()

		watchStarted := make(chan struct{})
		var watchStartedOnce sync.Once
		kubeClient := interceptor.NewClient(baseClient, interceptor.Funcs{
			Watch: func(
				ctx context.Context,
				client ctrlclient.WithWatch,
				list ctrlclient.ObjectList,
				opts ...ctrlclient.ListOption,
			) (k8swatch.Interface, error) {
				watchStartedOnce.Do(func() {
					close(watchStarted)
				})

				return client.Watch(ctx, list, opts...)
			},
		})

		deleteErrCh := make(chan error, 1)
		go func() {
			<-watchStarted

			route := &gatewayv1.HTTPRoute{}
			if err := baseClient.Get(t.Context(), key, route); err != nil {
				deleteErrCh <- err
				return
			}

			deleteErrCh <- baseClient.Delete(t.Context(), route)
		}()

		err := WaitForHTTPRouteDeleted(
			t.Context(),
			kubeClient,
			key.Namespace,
			key.Name,
			waitOpts,
		)
		require.NoError(t, err)
		require.NoError(t, <-deleteErrCh)
	})

	t.Run("waits for namespace deletion", func(t *testing.T) {
		fakerGen := faker.New()
		namespaceNames := []string{
			"oke-gw-e2e-" + fakerGen.UUID().V4(),
			"oke-gw-e2e-" + fakerGen.UUID().V4(),
		}
		scheme := makeTestScheme(t)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		err := WaitForNamespacesDeleted(
			t.Context(),
			kubeClient,
			namespaceNames,
			waitOpts,
		)
		require.NoError(t, err)
	})

	t.Run("waits for deployment availability", func(t *testing.T) {
		fakerGen := faker.New()
		namespaceName := "oke-gw-e2e-" + fakerGen.UUID().V4()
		deploymentName := "echo-" + fakerGen.UUID().V4()
		scheme := makeTestScheme(t)
		replicas := int32(1)
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       deploymentName,
					Namespace:  namespaceName,
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
			namespaceName,
			deploymentName,
			waitOpts,
		)
		require.NoError(t, err)
	})

	t.Run("waits for endpoint slices", func(t *testing.T) {
		fakerGen := faker.New()
		namespaceName := "oke-gw-e2e-" + fakerGen.UUID().V4()
		serviceName := "echo-" + fakerGen.UUID().V4()
		endpointSliceName := "echo-" + fakerGen.UUID().V4()
		scheme := makeTestScheme(t)
		ready := true
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      endpointSliceName,
					Namespace: namespaceName,
					Labels: map[string]string{
						discoveryv1.LabelServiceName: serviceName,
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
			namespaceName,
			serviceName,
			waitOpts,
		)
		require.NoError(t, err)
		require.Len(t, slices, 1)
		assert.Equal(t, endpointSliceName, slices[0].Name)
	})

	t.Run("waits for endpoint slices to stop publishing ready addresses", func(t *testing.T) {
		fakerGen := faker.New()
		namespaceName := "oke-gw-e2e-" + fakerGen.UUID().V4()
		serviceName := "echo-" + fakerGen.UUID().V4()
		endpointSliceName := "echo-" + fakerGen.UUID().V4()
		scheme := makeTestScheme(t)
		ready := false
		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      endpointSliceName,
					Namespace: namespaceName,
					Labels: map[string]string{
						discoveryv1.LabelServiceName: serviceName,
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
			namespaceName,
			serviceName,
			waitOpts,
		)
		require.NoError(t, err)
		require.Len(t, slices, 1)
		assert.Equal(t, endpointSliceName, slices[0].Name)
	})
}

func makeTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme, err := NewScheme()
	require.NoError(t, err)

	return scheme
}
