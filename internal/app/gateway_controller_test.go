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

	// Existing test - might need minor adjustments if the old setup is incompatible
	t.Run("Reconcile_SimpleGet", func(t *testing.T) {
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

		// Mock Get
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.Anything).
			RunAndReturn(func(_ context.Context, nn types.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
				assert.Equal(t, req.NamespacedName, nn)
				reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
				return nil
			})

		// Mock isConditionSet to return true (simulating already accepted)
		mockResourcesModel.EXPECT().
			isConditionSet(gateway, gateway.Status.Conditions, AcceptedConditionType).
			Return(true).Maybe() // Use Maybe() if this isn't the primary path tested here

		// Call Reconcile
		result, err := controller.Reconcile(t.Context(), req)

		// Assert results
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})

	// New Test Case
	t.Run("Reconcile_SetsAcceptedAndAnnotation", func(t *testing.T) {
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

		// Mock Get
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.Anything).
			RunAndReturn(func(_ context.Context, nn types.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
				assert.Equal(t, req.NamespacedName, nn)
				// Crucial: Ensure the receiver gets the Status field initialized
				// Use reflect to set the value, including the initialized Status
				reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gateway))
				return nil
			})

		// Mock isConditionSet to return false (not yet accepted)
		mockResourcesModel.EXPECT().
			isConditionSet(mock.AnythingOfType("*v1.Gateway"), mock.Anything, AcceptedConditionType).
			Run(func(resource client.Object, _ []metav1.Condition, _ string) {
				// Add assertions here if needed, e.g., check resource name/namespace
				assert.Equal(t, gateway.Name, resource.GetName())
			}).
			Return(false)

		// Mock setAcceptedCondition
		expectedMessage := fmt.Sprintf("Gateway %s accepted by controller class %s", gateway.Name, ControllerClassName)
		expectedAnnotations := map[string]string{
			"oke-gateway-api.oraclecloud.com/managed-by": ControllerClassName,
		}

		mockResourcesModel.EXPECT().
			setAcceptedCondition(t.Context(), setAcceptedConditionParams{
				resource:    gateway,
				conditions:  &gateway.Status.Conditions,
				message:     expectedMessage,
				annotations: expectedAnnotations,
			}).
			Return(nil) // Simulate successful update

		// Call Reconcile
		result, err := controller.Reconcile(t.Context(), req)

		// Assert results
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})
}
