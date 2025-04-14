package app

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
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

// Helper to create a Gateway with random data.
func newRandomGateway() *gatewayv1.Gateway {
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:       faker.DomainName(),
			Namespace:  faker.Username(), // Gateways are namespaced
			Generation: rand.Int64(),
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
		Status: gatewayv1.GatewayStatus{ // Initialize status
			Conditions: []metav1.Condition{},
		},
	}
}

func TestGatewayController(t *testing.T) {
	newMockDeps := func(t *testing.T) GatewayControllerDeps {
		return GatewayControllerDeps{
			K8sClient:      NewMockk8sClient(t),
			ResourcesModel: NewMockresourcesModel(t),
			GatewayModel:   NewMockgatewayModel(t),
			RootLogger:     diag.RootTestLogger(),
		}
	}

	t.Run("ReconcileValidGateway", func(t *testing.T) {
		// Create a test Gateway using the helper
		gateway := newRandomGateway()

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Namespace: gateway.Namespace,
				Name:      gateway.Name,
			},
		}

		deps := newMockDeps(t)
		controller := NewGatewayController(deps)

		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)
		mockGatewayModel, _ := deps.GatewayModel.(*MockgatewayModel)

		// Mock Get
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.Anything).
			RunAndReturn(func(_ context.Context, nn types.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
				assert.Equal(t, req.NamespacedName, nn)
				reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
				return nil
			})

		mockResourcesModel.EXPECT().
			isConditionSet(gateway, gateway.Status.Conditions, ProgrammedGatewayConditionType).
			Return(false).Once()

		mockGatewayModel.EXPECT().
			programGateway(t.Context(), gateway).
			Return(nil).Once()

		mockResourcesModel.EXPECT().
			setCondition(t.Context(), setConditionParams{
				resource:      gateway,
				conditions:    &gateway.Status.Conditions,
				conditionType: ProgrammedGatewayConditionType,
				status:        metav1.ConditionTrue,
				reason:        LoadBalancerReconciledReason,
				message:       fmt.Sprintf("Gateway %s programmed by %s", gateway.Name, ControllerClassName),
			}).
			Return(nil).Once()

		result, err := controller.Reconcile(t.Context(), req)

		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})
}
