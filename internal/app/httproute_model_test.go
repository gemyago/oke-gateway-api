package app

import (
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestHTTPRouteModelImpl(t *testing.T) {
	newMockDeps := func(t *testing.T) httpRouteModelDeps {
		return httpRouteModelDeps{
			K8sClient:  NewMockk8sClient(t),
			RootLogger: diag.RootTestLogger(),
		}
	}

	t.Run("gateway not accepted", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newHTTPRouteModel(deps)

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: faker.Word(),
				Name:      faker.Word(),
			},
		}

		var receiver acceptedHTTPRouteDetails
		accepted, err := model.acceptReconcileRequest(t.Context(), req, &receiver)

		require.NoError(t, err)
		assert.False(t, accepted, "Should return false when gateway is not accepted")
	})
}
