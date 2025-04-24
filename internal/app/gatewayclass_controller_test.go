package app

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"errors"

	"github.com/gemyago/oke-gateway-api/internal/diag"
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
	newMockDeps := func(t *testing.T) GatewayClassControllerDeps {
		return GatewayClassControllerDeps{
			K8sClient:      NewMockk8sClient(t),
			ResourcesModel: NewMockresourcesModel(t),
			RootLogger:     diag.RootTestLogger(),
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

		deps := newMockDeps(t)
		controller := NewGatewayClassController(deps)

		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)

		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.Anything).
			RunAndReturn(func(_ context.Context, nn types.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
				assert.Equal(t, req.NamespacedName, nn)
				reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gatewayClass))
				return nil
			})

		mockResourcesModel.EXPECT().
			isConditionSet(gatewayClass, gatewayClass.Status.Conditions, string(gatewayv1.GatewayClassConditionStatusAccepted)).
			Return(false)

		mockResourcesModel.EXPECT().
			setCondition(t.Context(), setConditionParams{
				resource:      gatewayClass,
				conditions:    &gatewayClass.Status.Conditions,
				conditionType: string(gatewayv1.GatewayClassConditionStatusAccepted),
				status:        metav1.ConditionTrue,
				reason:        string(gatewayv1.GatewayClassReasonAccepted),
				message:       fmt.Sprintf("GatewayClass %s is accepted by %s", gatewayClass.Name, ControllerClassName),
			}).
			Return(nil)

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

		deps := newMockDeps(t)
		controller := NewGatewayClassController(deps)

		mockClient, _ := deps.K8sClient.(*Mockk8sClient)

		// Simulate client returning NotFound error
		notFoundErr := apierrors.NewNotFound(gatewayv1.Resource("gatewayclasses"), req.Name)
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.AnythingOfType("*v1.GatewayClass")).
			Return(notFoundErr)

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

		deps := newMockDeps(t)
		controller := NewGatewayClassController(deps)

		mockClient, _ := deps.K8sClient.(*Mockk8sClient)

		// Simulate client returning a generic error
		getErr := errors.New(faker.Sentence())
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.AnythingOfType("*v1.GatewayClass")).
			Return(getErr)

		// Status should not be called if Get fails
		mockClient.EXPECT().Status().Maybe()

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

		deps := newMockDeps(t)
		controller := NewGatewayClassController(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)

		// Simulate successful Get
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.Anything).
			RunAndReturn(func(_ context.Context, nn types.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
				assert.Equal(t, req.NamespacedName, nn)
				reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gatewayClass))
				return nil
			})

		mockResourcesModel.EXPECT().
			isConditionSet(
				gatewayClass,
				gatewayClass.Status.Conditions,
				string(gatewayv1.GatewayClassConditionStatusAccepted),
			).
			Return(false)

		// Simulate Status Update error
		statusUpdateErr := errors.New(faker.Sentence())
		mockResourcesModel.EXPECT().
			setCondition(t.Context(), mock.Anything).
			Return(statusUpdateErr)

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

		deps := newMockDeps(t)
		controller := NewGatewayClassController(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)

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

		result, err := controller.Reconcile(t.Context(), req)

		// Expect no error and an empty result, as the controller should ignore this GatewayClass
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})

	t.Run("AlreadyAccepted", func(t *testing.T) {
		// Create a GatewayClass that is already accepted
		gatewayClass := newRandomGatewayClass()

		req := reconcile.Request{
			NamespacedName: client.ObjectKey{
				Name: gatewayClass.Name,
			},
		}

		deps := newMockDeps(t)
		controller := NewGatewayClassController(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockResourcesModel, _ := deps.ResourcesModel.(*MockresourcesModel)

		// Simulate successful Get returning the already-accepted object
		mockClient.EXPECT().
			Get(t.Context(), req.NamespacedName, mock.Anything).
			RunAndReturn(func(_ context.Context, nn types.NamespacedName, receiver client.Object, _ ...client.GetOption) error {
				assert.Equal(t, req.NamespacedName, nn)
				reflect.ValueOf(receiver).Elem().Set(reflect.ValueOf(*gatewayClass))
				return nil
			})

		// We expect the new isConditionSet method to be called and return true
		mockResourcesModel.EXPECT().
			isConditionSet(gatewayClass, gatewayClass.Status.Conditions, string(gatewayv1.GatewayClassConditionStatusAccepted)).
			Return(true)

		result, err := controller.Reconcile(t.Context(), req)

		// Expect no error and an empty result, as no update should be needed
		require.NoError(t, err)
		assert.Equal(t, reconcile.Result{}, result)
	})
}
