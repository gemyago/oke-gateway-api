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
				ControllerName: ControllerClassName,
			},
			Status: gatewayv1.GatewayClassStatus{
				Conditions: []metav1.Condition{},
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

				assert.False(t, acceptedCondition.LastTransitionTime.IsZero(), "LastTransitionTime should be set")

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
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
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
				Conditions: []metav1.Condition{},
			},
		}

		message := faker.Sentence()
		params := setConditionParams{
			resource:   gatewayClass,
			conditions: &gatewayClass.Status.Conditions,
			message:    message,
		}

		expectedError := errors.New(faker.Sentence())

		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().
			Update(mock.Anything, mock.AnythingOfType("*v1.GatewayClass"), mock.Anything).
			Return(expectedError)

		err := model.setCondition(t.Context(), params)

		require.Error(t, err, "Expected an error from setAcceptedCondition")
		require.ErrorIs(t, err, expectedError, "Returned error should wrap the original update error")
	})

	t.Run("HappyPath_AddsAnnotations", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		key1 := "key1-" + faker.Word()
		keyShared := "shared-" + faker.Word()
		key2 := "key2-" + faker.Word()
		val1 := faker.Sentence()
		valInitialShared := faker.Sentence()
		val2 := faker.Sentence()
		valNewShared := faker.Sentence()

		initialAnnotations := map[string]string{
			key1:      val1,
			keyShared: valInitialShared,
		}
		newAnnotations := map[string]string{
			key2:      val2,
			keyShared: valNewShared, // This should overwrite the initial shared value
		}
		expectedMergedAnnotations := map[string]string{
			key1:      val1,
			key2:      val2,
			keyShared: valNewShared,
		}

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        faker.DomainName(),
				Generation:  rand.Int64(),
				Annotations: initialAnnotations,
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
			Status: gatewayv1.GatewayClassStatus{
				Conditions: []metav1.Condition{},
			},
		}

		params := setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: faker.DomainName(),
			status:        metav1.ConditionTrue,
			reason:        faker.Word(),
			message:       faker.Sentence(),
			annotations:   newAnnotations,
		}

		updateCall := mockClient.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				gc, ok := obj.(*gatewayv1.GatewayClass)
				require.True(t, ok, "Object should be GatewayClass")
				assert.Equal(t, expectedMergedAnnotations, gc.GetAnnotations(), "Annotations should be merged")
				return true
			}), mock.Anything).Return(nil).Once()

		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()

		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				gc, ok := obj.(*gatewayv1.GatewayClass)
				require.True(t, ok, "Object should be GatewayClass for status update")
				assert.Equal(t, expectedMergedAnnotations, gc.GetAnnotations(), "Annotations should persist for Status Update")
				require.Len(t, gc.Status.Conditions, 1, "Expected one condition in status")
				cond := meta.FindStatusCondition(gc.Status.Conditions, params.conditionType)
				require.NotNil(t, cond)
				assert.Equal(t, params.status, cond.Status)
				assert.Equal(t, params.reason, cond.Reason)
				assert.Equal(t, params.message, cond.Message)
				assert.Equal(t, gatewayClass.Generation, cond.ObservedGeneration)
				return true
			}), mock.Anything).
			Return(nil).
			NotBefore(updateCall). // Ensure Status().Update() happens AFTER client.Update()
			Once()

		err := model.setCondition(t.Context(), params)
		require.NoError(t, err)
	})

	t.Run("HappyPath_AddsAnnotations_NoInitial", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		newAnnotations := map[string]string{
			"keyA": faker.Sentence(),
			"keyB": faker.Sentence(),
		}

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        faker.DomainName(),
				Generation:  rand.Int64(),
				Annotations: nil,
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
			Status: gatewayv1.GatewayClassStatus{
				Conditions: []metav1.Condition{},
			},
		}

		params := setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: faker.DomainName(),
			status:        metav1.ConditionTrue,
			reason:        faker.Word(),
			message:       faker.Sentence(),
			annotations:   newAnnotations,
		}

		updateCall := mockClient.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				gc, ok := obj.(*gatewayv1.GatewayClass)
				require.True(t, ok)
				assert.Equal(t, newAnnotations, gc.GetAnnotations(), "Annotations should match the new ones")
				return true
			}), mock.Anything).Return(nil).Once()

		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()

		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				gc, ok := obj.(*gatewayv1.GatewayClass)
				require.True(t, ok)
				assert.Equal(t, newAnnotations, gc.GetAnnotations(), "Annotations should persist for Status Update")
				require.Len(t, gc.Status.Conditions, 1)
				return true
			}), mock.Anything).
			Return(nil).
			NotBefore(updateCall).
			Once()

		err := model.setCondition(t.Context(), params)
		require.NoError(t, err)
	})

	t.Run("ErrorPath_AnnotationUpdateFails", func(t *testing.T) {
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        faker.DomainName(),
				Generation:  rand.Int64(),
				Annotations: map[string]string{"initial": faker.Word()},
			},
			Spec:   gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
			Status: gatewayv1.GatewayClassStatus{Conditions: []metav1.Condition{}},
		}

		params := setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: faker.DomainName(),
			status:        metav1.ConditionTrue,
			reason:        faker.Word(),
			message:       faker.Sentence(),
			annotations:   map[string]string{"new": faker.Word()},
		}

		expectedError := errors.New(faker.Sentence())

		mockClient.EXPECT().
			Update(t.Context(), gatewayClass, mock.Anything).
			Return(expectedError).Once()

		err := model.setCondition(t.Context(), params)

		require.Error(t, err, "Expected an error from setCondition due to Update failure")
		require.ErrorIs(t, err, expectedError, "Returned error should wrap the original Update error")
	})
}

func TestResourcesModelImpl_isConditionSet(t *testing.T) {
	newMockDeps := func(t *testing.T) resourcesModelDeps {
		return resourcesModelDeps{
			K8sClient:  NewMockk8sClient(t),
			RootLogger: diag.RootTestLogger(),
		}
	}

	type randomResourceOpt func(*gatewayv1.GatewayClass)
	newRandomResource := func(opts ...randomResourceOpt) *gatewayv1.GatewayClass {
		resource := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:       faker.DomainName(), // Use faker for name
				Generation: rand.Int64(),
			},
		}
		for _, opt := range opts {
			opt(resource)
		}
		return resource
	}

	randomResourceWithGeneration := func(generation int64) randomResourceOpt {
		return func(resource *gatewayv1.GatewayClass) {
			resource.Generation = generation
		}
	}

	randomResourceWithAnnotations := func(annotations map[string]string) randomResourceOpt {
		return func(resource *gatewayv1.GatewayClass) {
			resource.Annotations = annotations
		}
	}

	randomResourceWithConditions := func(conditions []metav1.Condition) randomResourceOpt {
		return func(resource *gatewayv1.GatewayClass) {
			resource.Status.Conditions = conditions
		}
	}

	type randomConditionsOpt func(*metav1.Condition)

	newRandomConditions := func(opts ...randomConditionsOpt) []metav1.Condition {
		condition := metav1.Condition{
			Type:               faker.DomainName(),
			Status:             metav1.ConditionTrue,
			Reason:             faker.Word(),
			ObservedGeneration: rand.Int64(),
		}
		for _, opt := range opts {
			opt(&condition)
		}
		return []metav1.Condition{condition}
	}

	randomConditionWithType := func(conditionType string) randomConditionsOpt {
		return func(condition *metav1.Condition) {
			condition.Type = conditionType
		}
	}

	randomConditionWithObservedGeneration := func(observedGeneration int64) randomConditionsOpt {
		return func(condition *metav1.Condition) {
			condition.ObservedGeneration = observedGeneration
		}
	}

	t.Run("ConditionSetAndMatches", func(t *testing.T) {
		model := newResourcesModel(newMockDeps(t))
		conditionType := faker.DomainName()
		generation := rand.Int64()
		gatewayClass := newRandomResource(
			randomResourceWithGeneration(generation),
			randomResourceWithConditions(
				newRandomConditions(
					randomConditionWithType(conditionType),
					randomConditionWithObservedGeneration(generation),
				),
			),
		)
		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    gatewayClass.Status.Conditions,
			conditionType: conditionType,
		}
		result := model.isConditionSet(params)
		assert.True(t, result, "Expected true when condition/generation match and no annotations requested")
	})

	t.Run("ConditionNotSet", func(t *testing.T) {
		model := newResourcesModel(newMockDeps(t))
		conditionType := faker.DomainName()
		gatewayClass := newRandomResource()

		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    newRandomConditions(),
			conditionType: conditionType,
		}
		result := model.isConditionSet(params)
		assert.False(t, result, "Expected false when conditions slice is empty")
	})

	t.Run("ConditionSet_WrongType", func(t *testing.T) {
		model := newResourcesModel(newMockDeps(t))
		conditionType := faker.DomainName()
		gatewayClass := newRandomResource(
			randomResourceWithConditions(
				newRandomConditions(),
			),
		)
		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    gatewayClass.Status.Conditions,
			conditionType: conditionType,
		}
		result := model.isConditionSet(params)
		assert.False(t, result, "Expected false for wrong condition type")
	})

	t.Run("ConditionSet_WrongGeneration", func(t *testing.T) {
		model := newResourcesModel(newMockDeps(t))
		conditionType := faker.DomainName()
		generation := rand.Int64()
		gatewayClass := newRandomResource(
			randomResourceWithGeneration(generation),
			randomResourceWithConditions(
				newRandomConditions(
					randomConditionWithType(conditionType),
					randomConditionWithObservedGeneration(generation+1),
				),
			),
		)
		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    gatewayClass.Status.Conditions,
			conditionType: conditionType,
		}
		result := model.isConditionSet(params)
		assert.False(t, result, "Expected false for wrong observed generation")
	})

	t.Run("ConditionSetAndMatches_WithMatchingAnnotations", func(t *testing.T) {
		model := newResourcesModel(newMockDeps(t))
		conditionType := faker.DomainName()
		generation := rand.Int64()

		key1 := "key1-" + faker.Word()
		key2 := "key2-" + faker.Word()
		val1 := faker.Sentence()
		val2 := faker.Sentence()

		resourceAnnotations := map[string]string{
			key1: val1,
			key2: val2,
		}
		paramsAnnotations := map[string]string{
			key1: val1,
			key2: val2,
		}
		gatewayClass := newRandomResource(
			randomResourceWithGeneration(generation),
			randomResourceWithAnnotations(resourceAnnotations),
			randomResourceWithConditions(
				newRandomConditions(
					randomConditionWithType(conditionType),
					randomConditionWithObservedGeneration(generation),
				),
			),
		)
		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    gatewayClass.Status.Conditions,
			conditionType: conditionType,
			annotations:   paramsAnnotations,
		}
		result := model.isConditionSet(params)
		assert.True(t, result, "Expected true when condition/gen/annotations all match")
	})

	t.Run("ConditionSetAndMatches_WithMissingAnnotation", func(t *testing.T) {
		model := newResourcesModel(newMockDeps(t))
		conditionType := faker.DomainName()
		generation := rand.Int64()
		gatewayClass := newRandomResource(
			randomResourceWithGeneration(generation),
			randomResourceWithAnnotations(map[string]string{}),
		)
		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    gatewayClass.Status.Conditions,
			conditionType: conditionType,
			annotations: map[string]string{
				"key1-" + faker.Word(): faker.Sentence(),
				"key2-" + faker.Word(): faker.Sentence(),
			},
		}
		result := model.isConditionSet(params)
		assert.False(t, result, "Expected false when a requested annotation value mismatches")
	})

	t.Run("ConditionSetAndMatches_WithMismatchedAnnotationValue", func(t *testing.T) {
		model := newResourcesModel(newMockDeps(t))
		conditionType := faker.DomainName()
		generation := rand.Int64()
		key := "key-" + faker.Word()
		gatewayClass := newRandomResource(
			randomResourceWithGeneration(generation),
			randomResourceWithAnnotations(map[string]string{key: faker.Sentence()}),
		)
		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    gatewayClass.Status.Conditions,
			conditionType: conditionType,
			annotations:   map[string]string{key: "other-" + faker.Sentence()},
		}
		result := model.isConditionSet(params)
		assert.False(t, result, "Expected false when a requested annotation value mismatches")
	})

	t.Run("ConditionSetAndMatches_WithExtraResourceAnnotation", func(t *testing.T) {
		model := newResourcesModel(newMockDeps(t))
		conditionType := faker.DomainName()
		generation := rand.Int64()
		key := "key-" + faker.Word()
		val := faker.Sentence()
		gatewayClass := newRandomResource(
			randomResourceWithGeneration(generation),
			randomResourceWithAnnotations(map[string]string{
				key:      val,
				"extra1": faker.Sentence(),
				"extra2": faker.Sentence(),
			}),
			randomResourceWithConditions(
				newRandomConditions(
					randomConditionWithType(conditionType),
					randomConditionWithObservedGeneration(generation),
				),
			),
		)
		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    gatewayClass.Status.Conditions,
			conditionType: conditionType,
			annotations:   map[string]string{key: val},
		}
		result := model.isConditionSet(params)
		assert.True(t, result, "Expected true when annotations param is nil")
	})
}
