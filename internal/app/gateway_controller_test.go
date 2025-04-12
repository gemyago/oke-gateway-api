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
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestGatewayController(t *testing.T) {
	t.Run("Reconcile", func(t *testing.T) {
		// Create a test Gateway
		gateway := &gatewayv1.Gateway{
			ObjectMeta: v1.ObjectMeta{
				Name:      faker.DomainName(),
				Namespace: faker.Username(), // Gateways are namespaced
			},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: gatewayv1.ObjectName(faker.DomainName()),
				Listeners: []gatewayv1.Listener{
					{
						Name:     "http",
						Port:     80,
						Protocol: gatewayv1.HTTPProtocolType,
					},
				},
			},
		}

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: gateway.Namespace,
				Name:      gateway.Name,
			},
		}

		mockClient := NewMockk8sClient(t) // Assuming NewMockk8sClient is defined elsewhere (e.g., test setup or another file)
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.Anything).
			RunAndReturn(func(_ context.Context, nn types.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
				assert.Equal(t, req.NamespacedName, nn)
				reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
				return nil
			})

		// Create the controller
		controller := &GatewayController{ // Use GatewayController
			client: mockClient,
			logger: diag.RootTestLogger(),
		}

		// Call Reconcile
		result, err := controller.Reconcile(t.Context(), req)

		// Assert results
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})
}
