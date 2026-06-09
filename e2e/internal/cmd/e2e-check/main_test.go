package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
	"github.com/gemyago/oke-gateway-api/e2e/internal/diag"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2ek8s"
	"github.com/gemyago/oke-gateway-api/e2e/internal/e2eoci"
)

func TestLoadCheckConfig(t *testing.T) {
	t.Parallel()

	cfg, err := loadCheckConfig(
		func(key string) (string, bool) {
			values := map[string]string{
				"OKE_E2E_LOAD_BALANCER_ID": "ocid1.loadbalancer.oc1..example",
				"OKE_E2E_KUBE_CONTEXT":     "oke-live",
				"KUBECONFIG":               "/tmp/kubeconfig",
				"OCI_CONFIG_FILE":          "/tmp/oci-config",
				"OCI_CLI_PROFILE":          "DEFAULT",
				"OKE_E2E_CONTROLLER_BIN":   "/tmp/controller",
			}

			value, ok := values[key]
			return value, ok
		},
		func(path string) (fs.FileInfo, error) {
			assert.Equal(t, "/tmp/controller", path)
			return fakeFileInfo{}, nil
		},
	)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "oke-gw-e2e-", cfg.NamespacePrefix)
	assert.Equal(t, "oke-live", cfg.Kubernetes.Context)
	assert.Equal(t, "/tmp/kubeconfig", cfg.Kubernetes.KubeconfigPath)
	assert.Equal(t, "ocid1.loadbalancer.oc1..example", cfg.OCI.LoadBalancerID)
	assert.Equal(t, "/tmp/oci-config", cfg.OCI.ConfigFile)
	assert.Equal(t, "DEFAULT", cfg.OCI.ConfigProfile)
	assert.Equal(t, "/tmp/controller", cfg.Controller.BinPath)
	assert.False(t, cfg.Controller.SkipStart)
}

func TestCheckKubernetesReadAccess(t *testing.T) {
	t.Parallel()

	makeCRD := func() *unstructured.Unstructured {
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "apiextensions.k8s.io",
			Version: "v1",
			Kind:    "CustomResourceDefinition",
		})
		crd.SetName(gatewayConfigCRDName)
		return crd
	}

	t.Run("passes when namespace reads and the GatewayConfig CRD exist", func(t *testing.T) {
		t.Parallel()

		scheme := makeTestScheme(t)
		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
				makeCRD(),
			).
			Build()

		err := checkKubernetesReadAccess(t.Context(), client, config.KubernetesConfig{Context: "oke-live"})
		require.NoError(t, err)
	})

	t.Run("fails when the GatewayConfig CRD is missing", func(t *testing.T) {
		t.Parallel()

		scheme := makeTestScheme(t)
		client := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}).
			Build()

		err := checkKubernetesReadAccess(t.Context(), client, config.KubernetesConfig{Context: "oke-live"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), gatewayConfigCRDName)
		assert.True(t, apierrors.IsNotFound(err))
	})
}

func TestRun(t *testing.T) {
	t.Parallel()

	t.Run("runs Kubernetes and OCI preflight checks", func(t *testing.T) {
		t.Parallel()

		scheme := makeTestScheme(t)
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "apiextensions.k8s.io",
			Version: "v1",
			Kind:    "CustomResourceDefinition",
		})
		crd.SetName(gatewayConfigCRDName)

		kubeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
				crd,
			).
			Build()

		var output bytes.Buffer
		logger := diag.SetupRootLogger(diag.NewRootLoggerOpts().WithOutput(&output))

		err := run(
			t.Context(),
			logger,
			runtimeDeps{
				loadConfig: func() (*config.Config, error) {
					return &config.Config{
						NamespacePrefix:  "oke-gw-e2e-",
						GatewayClassName: "oke-gateway-api-e2e",
						HTTPPort:         80,
						Kubernetes: config.KubernetesConfig{
							Context: "oke-live",
						},
						OCI: config.OCIConfig{
							LoadBalancerID: "ocid1.loadbalancer.oc1..example",
						},
						Controller: config.ControllerConfig{
							BinPath:   "/tmp/controller",
							SkipStart: false,
						},
					}, nil
				},
				newKubernetesClient: func(config.KubernetesConfig) (kubernetesReader, error) {
					return kubeClient, nil
				},
				newLoadBalancerInspector: func(
					config.OCIConfig,
					*slog.Logger,
				) (loadBalancerInspector, error) {
					return stubLoadBalancerInspector{
						loadBalancer: &e2eoci.DisposableLoadBalancer{
							ID:             "ocid1.loadbalancer.oc1..example",
							PublicIP:       "203.0.113.10",
							LifecycleState: loadbalancer.LoadBalancerLifecycleStateActive,
						},
					}, nil
				},
			},
		)
		require.NoError(t, err)
		assert.Contains(t, output.String(), "e2e preflight checks passed")
	})

	t.Run("wraps OCI inspection failures", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		logger := diag.SetupRootLogger(diag.NewRootLoggerOpts().WithOutput(&bytes.Buffer{}))

		err := run(
			t.Context(),
			logger,
			runtimeDeps{
				loadConfig: func() (*config.Config, error) {
					return &config.Config{
						Kubernetes: config.KubernetesConfig{
							Context: "oke-live",
						},
						OCI: config.OCIConfig{
							LoadBalancerID: "ocid1.loadbalancer.oc1..example",
						},
					}, nil
				},
				newKubernetesClient: func(config.KubernetesConfig) (kubernetesReader, error) {
					return stubKubernetesClient{}, nil
				},
				newLoadBalancerInspector: func(
					config.OCIConfig,
					*slog.Logger,
				) (loadBalancerInspector, error) {
					return stubLoadBalancerInspector{err: wantErr}, nil
				},
			},
		)
		require.Error(t, err)
		require.ErrorIs(t, err, wantErr)
		assert.Contains(t, err.Error(), "inspect OCI load balancer")
	})
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "controller" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() fs.FileMode  { return 0o755 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

type stubKubernetesClient struct{}

func (stubKubernetesClient) Get(
	_ context.Context,
	_ ctrlclient.ObjectKey,
	_ ctrlclient.Object,
	_ ...ctrlclient.GetOption,
) error {
	return nil
}

func (stubKubernetesClient) List(
	_ context.Context,
	list ctrlclient.ObjectList,
	_ ...ctrlclient.ListOption,
) error {
	if namespaces, ok := list.(*corev1.NamespaceList); ok {
		namespaces.Items = []corev1.Namespace{{ObjectMeta: metav1.ObjectMeta{Name: "default"}}}
	}

	return nil
}

type stubLoadBalancerInspector struct {
	loadBalancer *e2eoci.DisposableLoadBalancer
	err          error
}

func (s stubLoadBalancerInspector) Inspect(
	_ context.Context,
	_ string,
) (*e2eoci.DisposableLoadBalancer, error) {
	if s.err != nil {
		return nil, s.err
	}

	return s.loadBalancer, nil
}

func makeTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme, err := e2ek8s.NewScheme()
	require.NoError(t, err)
	return scheme
}
