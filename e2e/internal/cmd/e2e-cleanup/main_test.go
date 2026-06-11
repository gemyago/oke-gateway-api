package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCleanupConfig(t *testing.T) {
	t.Parallel()

	cfg, err := loadCleanupConfig(func(key string) (string, bool) {
		values := map[string]string{
			"OKE_E2E_LOAD_BALANCER_ID": "ocid1.loadbalancer.oc1..example",
			"OKE_E2E_KUBE_CONTEXT":     "oke-live",
			"KUBECONFIG":               "/tmp/kubeconfig",
			"OCI_CONFIG_FILE":          "/tmp/oci-config",
			"OCI_CLI_PROFILE":          "DEFAULT",
		}

		value, ok := values[key]
		return value, ok
	})
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "oke-gw-e2e-", cfg.NamespacePrefix)
	assert.Equal(t, "oke-live", cfg.Kubernetes.Context)
	assert.Equal(t, "/tmp/kubeconfig", cfg.Kubernetes.KubeconfigPath)
	assert.Equal(t, "ocid1.loadbalancer.oc1..example", cfg.OCI.LoadBalancerID)
	assert.Equal(t, "/tmp/oci-config", cfg.OCI.ConfigFile)
	assert.Equal(t, "DEFAULT", cfg.OCI.ConfigProfile)
	assert.True(t, cfg.Controller.SkipStart)
}
