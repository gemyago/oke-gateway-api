package app

import (
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/stretchr/testify/require"
)

func TestWatchesModel(t *testing.T) {
	makeMockDeps := func(t *testing.T) WatchesModelDeps {
		return WatchesModelDeps{
			K8sClient: NewMockk8sClient(t),
			Logger:    diag.RootTestLogger(),
		}
	}

	t.Run("indexHTTPRouteByBackendService", func(t *testing.T) {
		t.Run("build index of all backend refs", func(t *testing.T) {
			deps := makeMockDeps(t)
			model := NewWatchesModel(deps)

			httpRoute := makeRandomHTTPRoute()

			result := model.indexHTTPRouteByBackendService(&httpRoute)

			// Assert
			require.Nil(t, result, "Expected nil result from stub implementation")
		})
	})
}
