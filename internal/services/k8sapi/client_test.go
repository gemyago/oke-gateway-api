package k8sapi

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"

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

	t.Run("uses default kubeconfig path when kubeconfig env is empty", func(t *testing.T) {
		t.Setenv("KUBECONFIG", "")
		t.Setenv("HOME", t.TempDir())

		cfg, err := newConfig(ConfigDeps{
			RootLogger: diag.RootTestLogger(),
		})

		require.Nil(t, cfg)
		require.Error(t, err)
		require.ErrorContains(t, err, filepath.Join(".kube", "config"))
	})
}

func TestNewManager(t *testing.T) {
	t.Run("creates manager and client", func(t *testing.T) {
		manager, err := newManager(&rest.Config{
			Host: "https://127.0.0.1",
		})

		require.NoError(t, err)
		require.NotNil(t, manager)

		k8sClient := newClient(manager)
		require.NotNil(t, k8sClient)
		require.NotNil(t, k8sClient.Client)
	})

	t.Run("returns manager creation errors", func(t *testing.T) {
		manager, err := newManager(&rest.Config{
			Host: "://invalid-url",
		})

		require.Nil(t, manager)
		require.Error(t, err)
	})

	t.Run("wraps scheme registration errors", func(t *testing.T) {
		noopInstaller := func(*runtime.Scheme) error {
			return nil
		}
		failingInstaller := func(wantErr error) func(*runtime.Scheme) error {
			return func(*runtime.Scheme) error {
				return wantErr
			}
		}

		tests := []struct {
			name    string
			index   int
			message string
		}{
			{
				name:    "kubernetes scheme",
				index:   0,
				message: "failed to add kubernetes scheme",
			},
			{
				name:    "known types scheme",
				index:   1,
				message: "failed to add gateway api scheme",
			},
			{
				name:    "gateway v1 scheme",
				index:   2,
				message: "failed to add gateway api scheme",
			},
			{
				name:    "gateway v1beta1 scheme",
				index:   3,
				message: "failed to add gateway api v1beta1 scheme",
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				wantErr := errors.New("scheme install failed")
				installers := []func(*runtime.Scheme) error{
					noopInstaller,
					noopInstaller,
					noopInstaller,
					noopInstaller,
				}
				installers[tc.index] = failingInstaller(wantErr)

				manager, err := newManagerWithSchemeInstallers(
					&rest.Config{Host: "https://127.0.0.1"},
					installers,
				)

				require.Nil(t, manager)
				require.ErrorIs(t, err, wantErr)
				require.ErrorContains(t, err, tc.message)
			})
		}
	})
}
