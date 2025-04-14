package app

import (
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	k8sapi "github.com/gemyago/oke-gateway-api/internal/services/k8sapi"
)

func TestResourcesModelImpl_setAcceptedCondition(t *testing.T) {
	newMockDeps := func(t *testing.T) resourcesModelDeps {
		return resourcesModelDeps{
			K8sClient:  NewMockk8sClient(t),
			RootLogger: diag.RootTestLogger(),
		}
	}

	t.Run("HappyPath_AddNewCondition", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newResourcesModel(deps)

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:       faker.DomainName(),
				Generation: rand.Int64(),
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName, // Use the constant
			},
			Status: gatewayv1.GatewayClassStatus{
				Conditions: []metav1.Condition{}, // Start with no conditions
			},
		}

		message := faker.Sentence()
		params := setAcceptedConditionParams{
			resource:   gatewayClass,
			conditions: &gatewayClass.Status.Conditions,
			message:    message,
		}

		timeBeforeAct := metav1.Now()

		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter)

		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(gc client.Object) bool {
				timeAfterAct := metav1.Now()

				// Check the condition was set correctly
				require.Len(t, gatewayClass.Status.Conditions, 1, "Expected exactly one condition")

				acceptedCondition := meta.FindStatusCondition(gatewayClass.Status.Conditions, AcceptedConditionType)
				require.NotNil(t, acceptedCondition, "Accepted condition should be found")

				assert.Equal(t, metav1.ConditionTrue, acceptedCondition.Status, "Condition status should be True")
				assert.Equal(t, AcceptedConditionReason, acceptedCondition.Reason, "Condition reason should be Accepted")
				assert.Equal(t, message, acceptedCondition.Message, "Condition message mismatch")
				assert.Equal(t,
					gatewayClass.Generation,
					acceptedCondition.ObservedGeneration,
					"ObservedGeneration should match resource generation")

				// Check timestamp was set recently
				assert.False(t, acceptedCondition.LastTransitionTime.IsZero(), "LastTransitionTime should be set")

				// Ensure the timestamp is within the bounds of the function call
				assert.True(t,
					!acceptedCondition.LastTransitionTime.Before(&timeBeforeAct) &&
						!acceptedCondition.LastTransitionTime.Time.After(timeAfterAct.Time),
					"Expected LTT between %v and %v, got %v", timeBeforeAct, timeAfterAct, acceptedCondition.LastTransitionTime)

				return assert.Same(t, gc, gatewayClass)
			}), mock.Anything).
			Return(nil)

		err := model.setAcceptedCondition(t.Context(), params)
		require.NoError(t, err)
	})

	t.Run("ErrorPath_StatusUpdateFails", func(t *testing.T) {
		// Arrange
		deps := newMockDeps(t)
		model := newResourcesModel(deps)

		// Get the mock client instance from the deps returned by the helper
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)

		// Create a mock status writer separately
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:       faker.DomainName(),
				Generation: 1,
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
			Status: gatewayv1.GatewayClassStatus{
				Conditions: []metav1.Condition{}, // Start with no conditions
			},
		}

		message := faker.Sentence()
		params := setAcceptedConditionParams{
			resource:   gatewayClass,
			conditions: &gatewayClass.Status.Conditions,
			message:    message,
		}

		expectedError := errors.New(faker.Sentence())

		// Expect Status().Update() to be called and fail
		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().
			Update(mock.Anything, mock.AnythingOfType("*v1.GatewayClass"), mock.Anything).
			Return(expectedError)

		// Act
		err := model.setAcceptedCondition(t.Context(), params)

		// Assert
		require.Error(t, err, "Expected an error from setAcceptedCondition")
		require.ErrorIs(t, err, expectedError, "Returned error should wrap the original update error")
	})

	t.Run("HappyPath_UpdateAnnotationsAndStatus", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		initialAnnotations := map[string]string{
			"1-" + faker.Word(): faker.Word(),
			"2-" + faker.Word(): faker.Word(),
		}
		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        faker.DomainName(),
				Generation:  rand.Int64(),
				Annotations: initialAnnotations,
			},
			Spec: gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
			Status: gatewayv1.GatewayClassStatus{
				Conditions: []metav1.Condition{},
			},
		}

		newAnnotations := map[string]string{
			"3-" + faker.Word(): faker.Word(),
			"4-" + faker.Word(): faker.Word(),
		}
		expectedAnnotations := make(map[string]string)
		for k, v := range initialAnnotations {
			expectedAnnotations[k] = v
		}
		for k, v := range newAnnotations {
			expectedAnnotations[k] = v
		}
		message := faker.Sentence()
		params := setAcceptedConditionParams{
			resource:    gatewayClass,
			conditions:  &gatewayClass.Status.Conditions,
			message:     message,
			annotations: newAnnotations,
		}

		// Expect Resource Update (for annotations)
		mockClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(gc client.Object) bool {
			assert.Equal(t, expectedAnnotations, gc.GetAnnotations(), "Annotations mismatch in resource update")
			return assert.Same(t, gc, gatewayClass) // Ensure it's the same object instance
		}), mock.Anything).Return(nil)

		// Expect Status Update (after resource update)
		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().Update(t.Context(), mock.MatchedBy(func(gc client.Object) bool {
			require.Len(t, gatewayClass.Status.Conditions, 1, "Expected exactly one condition after status update")
			acceptedCondition := meta.FindStatusCondition(gatewayClass.Status.Conditions, AcceptedConditionType)
			require.NotNil(t, acceptedCondition, "Accepted condition should be found after status update")
			assert.Equal(t, metav1.ConditionTrue, acceptedCondition.Status)
			assert.Equal(t, message, acceptedCondition.Message)
			assert.Equal(t, gatewayClass.Generation, acceptedCondition.ObservedGeneration)
			return assert.Same(t, gc, gatewayClass) // Ensure it's the same object instance
		}), mock.Anything).Return(nil)

		err := model.setAcceptedCondition(t.Context(), params)
		require.NoError(t, err)
	})

	t.Run("ErrorPath_ResourceUpdateFails", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		// No need for mockStatusWriter as Status().Update() shouldn't be called

		gatewayClass := &gatewayv1.GatewayClass{ /* ... minimal setup ... */ }
		newAnnotations := map[string]string{"fail": "update"}
		params := setAcceptedConditionParams{
			resource:    gatewayClass,
			conditions:  &gatewayClass.Status.Conditions, // Still needed for the func signature
			message:     faker.Sentence(),
			annotations: newAnnotations,
		}
		expectedError := errors.New(faker.Sentence())

		// Expect Resource Update to fail
		mockClient.EXPECT().Update(t.Context(), mock.Anything, mock.Anything).Return(expectedError)

		// Status().Update() should NOT be called

		err := model.setAcceptedCondition(t.Context(), params)
		require.Error(t, err)
		require.ErrorIs(t, err, expectedError, "Error from resource update should be returned")
	})

	t.Run("ErrorPath_StatusUpdateFailsAfterResourceUpdate", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		gatewayClass := &gatewayv1.GatewayClass{ /* ... minimal setup ... */ }
		newAnnotations := map[string]string{"succeed": "resource", "fail": "status"}
		params := setAcceptedConditionParams{
			resource:    gatewayClass,
			conditions:  &gatewayClass.Status.Conditions,
			message:     faker.Sentence(),
			annotations: newAnnotations,
		}
		expectedStatusError := errors.New(faker.Sentence())

		// Expect Resource Update to succeed
		mockClient.EXPECT().Update(t.Context(), mock.Anything, mock.Anything).Return(nil)

		// Expect Status Update to fail
		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().Update(t.Context(), mock.Anything, mock.Anything).Return(expectedStatusError)

		err := model.setAcceptedCondition(t.Context(), params)
		require.Error(t, err)
		require.ErrorIs(t, err, expectedStatusError, "Error from status update should be returned")
	})
}

func TestResourcesModelImpl_isConditionSet(t *testing.T) {
	newMockDeps := func(t *testing.T) resourcesModelDeps {
		return resourcesModelDeps{
			K8sClient:  NewMockk8sClient(t), // Mock client might not be strictly needed here but kept for consistency
			RootLogger: diag.RootTestLogger(),
		}
	}

	model := newResourcesModel(newMockDeps(t))

	// Shared setup for resource
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:       faker.DomainName(),
			Generation: rand.Int64(), // Set a specific generation
		},
	}

	t.Run("ConditionIsSetAndMatches", func(t *testing.T) {
		conditions := []metav1.Condition{
			{
				Type:               AcceptedConditionType,
				Status:             metav1.ConditionTrue,
				Reason:             AcceptedConditionReason,
				ObservedGeneration: gatewayClass.Generation, // Matches resource generation
			},
		}

		result := model.isConditionSet(gatewayClass, conditions, AcceptedConditionType)
		assert.True(t, result, "Expected isConditionSet to return true when condition matches")
	})

	t.Run("ConditionNotSet", func(t *testing.T) {
		conditions := []metav1.Condition{} // No conditions
		result := model.isConditionSet(gatewayClass, conditions, AcceptedConditionType)
		assert.False(t, result, "Expected isConditionSet to return false when conditions slice is empty")
	})

	t.Run("ConditionSet_WrongType", func(t *testing.T) {
		conditions := []metav1.Condition{
			{
				Type:               "SomeOtherType",
				Status:             metav1.ConditionTrue,
				Reason:             AcceptedConditionReason,
				ObservedGeneration: gatewayClass.Generation,
			},
		}
		result := model.isConditionSet(gatewayClass, conditions, AcceptedConditionType)
		assert.False(t, result, "Expected isConditionSet to return false for wrong condition type")
	})

	t.Run("ConditionSet_WrongStatus", func(t *testing.T) {
		conditions := []metav1.Condition{
			{
				Type:               AcceptedConditionType,
				Status:             metav1.ConditionFalse,
				Reason:             AcceptedConditionReason,
				ObservedGeneration: gatewayClass.Generation,
			},
		}
		result := model.isConditionSet(gatewayClass, conditions, AcceptedConditionType)
		assert.False(t, result, "Expected isConditionSet to return false for wrong condition status")
	})

	t.Run("ConditionSet_WrongGeneration", func(t *testing.T) {
		conditions := []metav1.Condition{
			{
				Type:               AcceptedConditionType,
				Status:             metav1.ConditionTrue,
				Reason:             AcceptedConditionReason,
				ObservedGeneration: gatewayClass.Generation - 1, // Mismatched generation
			},
		}
		result := model.isConditionSet(gatewayClass, conditions, AcceptedConditionType)
		assert.False(t, result, "Expected isConditionSet to return false for wrong observed generation")
	})
}

func TestResourcesModelImpl_setNotAcceptedCondition(t *testing.T) {
	newMockDeps := func(t *testing.T) resourcesModelDeps {
		return resourcesModelDeps{
			K8sClient:  NewMockk8sClient(t),
			RootLogger: diag.RootTestLogger(),
		}
	}

	newResource := func() *gatewayv1.Gateway {
		return &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Name:       faker.DomainName(),
				Namespace:  faker.Word(),
				Generation: rand.Int64(),
			},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: gatewayv1.ObjectName(faker.DomainName()),
			},
			Status: gatewayv1.GatewayStatus{
				Conditions: []metav1.Condition{},
			},
		}
	}

	t.Run("HappyPath_AddNewCondition", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newResourcesModel(deps)

		gatewayClass := newResource()

		params := setNotAcceptedConditionParams{
			resource:   gatewayClass,
			conditions: &gatewayClass.Status.Conditions,
			reason:     faker.Sentence(),
			message:    faker.Sentence(),
		}

		timeBeforeAct := metav1.Now()

		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter)

		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(gc client.Object) bool {
				timeAfterAct := metav1.Now()

				// Check the condition was set correctly
				require.Len(t, gatewayClass.Status.Conditions, 1, "Expected exactly one condition")

				acceptedCondition := meta.FindStatusCondition(gatewayClass.Status.Conditions, AcceptedConditionType)
				require.NotNil(t, acceptedCondition, "Accepted condition should be found")

				assert.Equal(t, metav1.ConditionFalse, acceptedCondition.Status, "Condition status should be True")
				assert.Equal(t, params.reason, acceptedCondition.Reason, "Condition reason should be Accepted")
				assert.Equal(t, params.message, acceptedCondition.Message, "Condition message mismatch")
				assert.Equal(t,
					gatewayClass.Generation,
					acceptedCondition.ObservedGeneration,
					"ObservedGeneration should match resource generation")

				// Check timestamp was set recently
				assert.False(t, acceptedCondition.LastTransitionTime.IsZero(), "LastTransitionTime should be set")

				// Ensure the timestamp is within the bounds of the function call
				assert.True(t,
					!acceptedCondition.LastTransitionTime.Before(&timeBeforeAct) &&
						!acceptedCondition.LastTransitionTime.Time.After(timeAfterAct.Time),
					"Expected LTT between %v and %v, got %v", timeBeforeAct, timeAfterAct, acceptedCondition.LastTransitionTime)

				return assert.Same(t, gc, gatewayClass)
			}), mock.Anything).
			Return(nil)

		err := model.setNotAcceptedCondition(t.Context(), params)
		require.NoError(t, err)
	})
}
