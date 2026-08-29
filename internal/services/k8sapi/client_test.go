package k8sapi

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

func TestNewConfigNoop(t *testing.T) {
	cfg, err := newConfig(ConfigDeps{
		RootLogger: diag.RootTestLogger(),
		Noop:       true,
	})

	require.NoError(t, err)
	require.NotNil(t, cfg)
}

func TestNewConfigErrors(t *testing.T) {
	t.Run("wraps in-cluster config errors", func(t *testing.T) {
		cfg, err := newConfig(ConfigDeps{
			RootLogger: diag.RootTestLogger(),
			InCluster:  true,
		})

		require.Nil(t, cfg)
		require.ErrorContains(t, err, "failed to get in-cluster config")
	})

	t.Run("returns kubeconfig load errors", func(t *testing.T) {
		t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing-kubeconfig"))

		cfg, err := newConfig(ConfigDeps{
			RootLogger: diag.RootTestLogger(),
		})

		require.Nil(t, cfg)
		require.Error(t, err)
	})
}
