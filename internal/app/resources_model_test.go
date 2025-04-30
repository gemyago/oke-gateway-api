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

func TestResourcesModelImpl_setCondition(t *testing.T) {
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
		params := setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: faker.DomainName(),
			status:        metav1.ConditionTrue,
			reason:        faker.Sentence(),
			message:       message,
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

				acceptedCondition := meta.FindStatusCondition(gatewayClass.Status.Conditions, params.conditionType)
				require.NotNil(t, acceptedCondition, "condition should be found")

				assert.Equal(t, metav1.ConditionTrue, acceptedCondition.Status, "Condition status should be True")
				assert.Equal(t, params.reason, acceptedCondition.Reason, "Condition reason should be valid")
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

		err := model.setCondition(t.Context(), params)
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
		params := setConditionParams{
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
		err := model.setCondition(t.Context(), params)

		// Assert
		require.Error(t, err, "Expected an error from setAcceptedCondition")
		require.ErrorIs(t, err, expectedError, "Returned error should wrap the original update error")
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
		conditionType := faker.DomainName()
		conditions := []metav1.Condition{
			{
				Type:               conditionType,
				Status:             metav1.ConditionTrue,
				Reason:             faker.Word(),
				ObservedGeneration: gatewayClass.Generation, // Matches resource generation
			},
		}

		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    conditions,
			conditionType: conditionType,
		}
		result := model.isConditionSet(params)
		assert.True(t, result, "Expected isConditionSet to return true when condition matches")
	})

	t.Run("ConditionNotSet", func(t *testing.T) {
		conditions := []metav1.Condition{} // No conditions
		conditionType := faker.DomainName()
		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    conditions,
			conditionType: conditionType,
		}
		result := model.isConditionSet(params)
		assert.False(t, result, "Expected isConditionSet to return false when conditions slice is empty")
	})

	t.Run("ConditionSet_WrongType", func(t *testing.T) {
		conditionType := faker.DomainName()
		conditions := []metav1.Condition{
			{
				Type:               "wrong-" + faker.DomainName(),
				Status:             metav1.ConditionTrue,
				Reason:             faker.Word(),
				ObservedGeneration: gatewayClass.Generation,
			},
		}
		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    conditions,
			conditionType: conditionType,
		}
		result := model.isConditionSet(params)
		assert.False(t, result, "Expected isConditionSet to return false for wrong condition type")
	})

	t.Run("ConditionSet_WrongGeneration", func(t *testing.T) {
		conditionType := faker.DomainName()
		conditions := []metav1.Condition{
			{
				Type:               conditionType,
				Status:             metav1.ConditionTrue,
				Reason:             faker.Word(),
				ObservedGeneration: gatewayClass.Generation - 1, // Mismatched generation
			},
		}
		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    conditions,
			conditionType: conditionType,
		}
		result := model.isConditionSet(params)
		assert.False(t, result, "Expected isConditionSet to return false for wrong observed generation")
	})
}
