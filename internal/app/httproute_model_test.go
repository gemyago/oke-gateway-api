package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestHTTPRouteModelImpl(t *testing.T) {
	newMockDeps := func(t *testing.T) httpRouteModelDeps {
		return httpRouteModelDeps{
			K8sClient:  NewMockk8sClient(t),
			RootLogger: diag.RootTestLogger(),
		}
	}

	setupClientGet := func(
		t *testing.T,
		cl k8sClient,
		wantName types.NamespacedName,
		wantObj interface{},
	) {
		mockK8sClient, _ := cl.(*Mockk8sClient)
		mockK8sClient.EXPECT().Get(
			t.Context(),
			wantName,
			mock.Anything,
		).RunAndReturn(func(
			_ context.Context,
			name types.NamespacedName,
			obj client.Object,
			_ ...client.GetOption,
		) error {
			assert.Equal(t, wantName, name)
			reflect.ValueOf(obj).Elem().Set(reflect.ValueOf(wantObj))
			return nil
		})
	}

	t.Run("resolveRequestParent", func(t *testing.T) {
		t.Run("relevant parent", func(t *testing.T) {
			deps := newMockDeps(t)
			model := newHTTPRouteModel(deps)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: faker.Word(),
					Name:      faker.Word(),
				},
			}
			route := makeRandomHTTPRoute()

			setupClientGet(t, deps.K8sClient, req.NamespacedName, route)

			// gatewayData := makeRandomAcceptedGatewayDetails()

			var receiver resolvedRouteParentDetails
			accepted, err := model.resolveRequestParent(t.Context(), req, &receiver)

			require.NoError(t, err)
			assert.True(t, accepted, "parent should be resolved")

			assert.Equal(t, receiver.httpRoute, route)
		})
	})
}
