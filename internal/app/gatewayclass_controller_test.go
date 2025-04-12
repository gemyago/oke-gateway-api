package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	k8sapi "github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestGatewayClassController(t *testing.T) {
	t.Run("Reconcile", func(t *testing.T) {
		// Create a test GatewayClass
		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: faker.DomainName(),
				Generation: func() int64 {
					randInts, err := faker.RandomInt(1, 10, 1) // Get one random int between 1 and 10
					require.NoError(t, err)                    // Fail test if faker errors
					require.Len(t, randInts, 1)                // Ensure we got one int
					return int64(randInts[0])
				}(),
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: "oracle.com/oke-gateway-controller",
			},
		}

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: faker.DomainName(),
				Name:      gatewayClass.Name,
			},
		}

		mockClient := NewMockk8sClient(t)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.Anything).
			RunAndReturn(func(_ context.Context, nn types.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
				assert.Equal(t, req.NamespacedName, nn)
				reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gatewayClass))
				return nil
			})

		mockClient.EXPECT().Status().Return(mockStatusWriter)

		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(gc *gatewayv1.GatewayClass) bool {
				if len(gc.Status.Conditions) != 1 {
					return false
				}
				condition := gc.Status.Conditions[0]
				return condition.Type == "Accepted" &&
					condition.Status == metav1.ConditionTrue &&
					condition.Reason == "Accepted" &&
					condition.ObservedGeneration == gatewayClass.Generation
			}), mock.Anything).
			Return(nil)

		controller := &GatewayClassController{
			client: mockClient,
			logger: diag.RootTestLogger(),
		}

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})
}
