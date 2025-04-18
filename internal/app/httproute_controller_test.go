package app_test

import (
	"testing"

	"log/slog"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/gemyago/oke-gateway-api/internal/app"
)

func TestHTTPRouteController_Reconcile_NotImplemented(t *testing.T) {
	controller := app.NewHTTPRouteController(app.HTTPRouteControllerDeps{
		// Provide mock/dummy dependencies if needed for initialization,
		// but for a simple "not implemented" check, nil/defaults might suffice.
		RootLogger: slog.Default(), // Use default logger for simplicity
		// K8sClient:      nil, // No client interaction expected yet
		// ResourcesModel: nil, // No model interaction expected yet
	})

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "test-ns", Name: "test-route"},
	}

	_, err := controller.Reconcile(t.Context(), req)

	require.Error(t, err)
	assert.EqualError(t, err, "not implemented")
}
