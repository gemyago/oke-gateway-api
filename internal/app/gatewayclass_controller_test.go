package app

import (
	"context"
	"math/rand/v2"
	"reflect"
	"testing"

	"errors"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	k8sapi "github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestGatewayClassController(t *testing.T) {
	// Helper to create a GatewayClass with random data
	newRandomGatewayClass := func() *gatewayv1.GatewayClass {
		return &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:            faker.DomainName(),
				Generation:      rand.Int64(),
				UID:             types.UID(faker.UUIDHyphenated()), // Add UID for potential future use
				ResourceVersion: faker.Word(),                      // Add RV for potential future use
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
		}
	}

	t.Run("Reconcile", func(t *testing.T) {
		// Create a test GatewayClass using the helper
		gatewayClass := newRandomGatewayClass()

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

	t.Run("NotFound", func(t *testing.T) {
		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Name: faker.DomainName(),
			},
		}

		mockClient := NewMockk8sClient(t)

		// Simulate client returning NotFound error
		notFoundErr := apierrors.NewNotFound(gatewayv1.Resource("gatewayclasses"), req.Name)
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.AnythingOfType("*v1.GatewayClass")).
			Return(notFoundErr)

		controller := &GatewayClassController{
			client: mockClient,
			logger: diag.RootTestLogger(),
		}

		result, err := controller.Reconcile(t.Context(), req)

		// Expect no error and an empty result when NotFound
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})

	t.Run("GetError", func(t *testing.T) {
		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Name: faker.DomainName(),
			},
		}

		mockClient := NewMockk8sClient(t)

		// Simulate client returning a generic error
		getErr := errors.New(faker.Sentence())
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.AnythingOfType("*v1.GatewayClass")).
			Return(getErr)

		// Status should not be called if Get fails
		mockClient.EXPECT().Status().Maybe()

		controller := &GatewayClassController{
			client: mockClient,
			logger: diag.RootTestLogger(),
		}

		result, err := controller.Reconcile(t.Context(), req)

		// Expect the specific error to be returned, wrapped
		require.Error(t, err)
		require.ErrorIs(t, err, getErr)             // Check if the original error is wrapped
		assert.Equal(t, reconcile.Result{}, result) // Expect empty result on error
	})

	t.Run("StatusUpdateError", func(t *testing.T) {
		// Create a test GatewayClass
		gatewayClass := newRandomGatewayClass()
		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Name: gatewayClass.Name,
			},
		}

		mockClient := NewMockk8sClient(t)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		// Simulate successful Get
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.Anything).
			RunAndReturn(func(_ context.Context, nn types.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
				assert.Equal(t, req.NamespacedName, nn)
				reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gatewayClass))
				return nil
			})

		mockClient.EXPECT().Status().Return(mockStatusWriter)

		// Simulate Status Update error
		statusUpdateErr := errors.New(faker.Sentence())
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.GatewayClass"), mock.Anything).
			Return(statusUpdateErr)

		controller := &GatewayClassController{
			client: mockClient,
			logger: diag.RootTestLogger(),
		}

		result, err := controller.Reconcile(t.Context(), req)

		// Expect the specific error from status update to be returned, wrapped
		require.Error(t, err)
		require.ErrorIs(t, err, statusUpdateErr)    // Check if the original error is wrapped
		assert.Equal(t, reconcile.Result{}, result) // Expect empty result on error
	})

	t.Run("WrongControllerName", func(t *testing.T) {
		// Create a GatewayClass with a controller name this controller shouldn't manage
		gatewayClass := newRandomGatewayClass()
		gatewayClass.Spec.ControllerName = gatewayv1.GatewayController(faker.DomainName()) // Different controller name

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Name: gatewayClass.Name,
			},
		}

		mockClient := NewMockk8sClient(t)

		// Simulate successful Get
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.Anything).
			RunAndReturn(func(_ context.Context, nn types.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
				assert.Equal(t, req.NamespacedName, nn)
				reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gatewayClass))
				return nil
			})

		// We DO NOT expect Status() or Update() to be called for a GatewayClass with the wrong controller name.
		// Testify's AssertExpectations (called via t.Cleanup) will fail the test if Status() is called.

		controller := &GatewayClassController{
			client: mockClient,
			logger: diag.RootTestLogger(),
		}

		result, err := controller.Reconcile(t.Context(), req)

		// Expect no error and an empty result, as the controller should ignore this GatewayClass
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})
}
