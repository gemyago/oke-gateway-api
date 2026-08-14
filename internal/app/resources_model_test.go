package app

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"

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
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:       fake.Internet().Domain(),
				Generation: rand.Int64(),
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
			Status: gatewayv1.GatewayClassStatus{
				Conditions: []metav1.Condition{},
			},
		}

		message := fake.Lorem().Sentence(10)
		params := setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: fake.Internet().Domain(),
			status:        metav1.ConditionTrue,
			reason:        fake.Lorem().Sentence(10),
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

				assert.True(
					t,
					!acceptedCondition.LastTransitionTime.Before(&timeBeforeAct) &&
						!acceptedCondition.LastTransitionTime.Time.After(timeAfterAct.Time),
					"Expected LTT between %v and %v, got %v",
					timeBeforeAct,
					timeAfterAct,
					acceptedCondition.LastTransitionTime,
				)

				return assert.Same(t, gc, gatewayClass)
			}), mock.Anything).
			Return(nil)

		err := model.setCondition(t.Context(), params)
		require.NoError(t, err)
	})

	t.Run("ErrorPath_StatusUpdateFails", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:       fake.Internet().Domain(),
				Generation: 1,
			},
			Spec: gatewayv1.GatewayClassSpec{
				ControllerName: ControllerClassName,
			},
			Status: gatewayv1.GatewayClassStatus{
				Conditions: []metav1.Condition{},
			},
		}

		message := fake.Lorem().Sentence(10)
		params := setConditionParams{
			resource:   gatewayClass,
			conditions: &gatewayClass.Status.Conditions,
			message:    message,
		}

		expectedError := errors.New(fake.Lorem().Sentence(10))

		mockClient.EXPECT().Status().Return(mockStatusWriter)
		mockStatusWriter.EXPECT().
			Update(mock.Anything, mock.AnythingOfType("*v1.GatewayClass"), mock.Anything).
			Return(expectedError)

		err := model.setCondition(t.Context(), params)

		require.Error(t, err, "Expected an error from setAcceptedCondition")
		require.ErrorIs(t, err, expectedError, "Returned error should wrap the original update error")
	})

	t.Run("HappyPath_AddsAnnotations", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		key1 := "key1-" + fake.Lorem().Word()
		keyShared := "shared-" + fake.Lorem().Word()
		key2 := "key2-" + fake.Lorem().Word()
		val1 := fake.Lorem().Sentence(10)
		valInitialShared := fake.Lorem().Sentence(10)
		val2 := fake.Lorem().Sentence(10)
		valNewShared := fake.Lorem().Sentence(10)

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
				Name:        fake.Internet().Domain(),
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
			conditionType: fake.Internet().Domain(),
			status:        metav1.ConditionTrue,
			reason:        fake.Lorem().Word(),
			message:       fake.Lorem().Sentence(10),
			annotations:   newAnnotations,
		}

		updateStatusCall := mockStatusWriter.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				gc, ok := obj.(*gatewayv1.GatewayClass)
				require.True(t, ok, "Object should be GatewayClass for status update")
				assert.Equal(t, initialAnnotations, gc.GetAnnotations())
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
			Once()

		mockClient.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				gc, ok := obj.(*gatewayv1.GatewayClass)
				require.True(t, ok, "Object should be GatewayClass")
				assert.Equal(t, expectedMergedAnnotations, gc.GetAnnotations(), "Annotations should be merged")
				return true
			}), mock.Anything).Return(nil).Once().NotBefore(updateStatusCall)

		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()

		err := model.setCondition(t.Context(), params)
		require.NoError(t, err)
	})

	t.Run("RemovesStaleFrontendMTLSDependencyAnnotation", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		keepKey := "keep-" + fake.Lorem().Word()
		keepValue := fake.UUID().V4()
		newRevision := fake.UUID().V4()
		gateway := &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:  "ns-" + fake.Lorem().Word(),
				Name:       "gateway-" + fake.Lorem().Word(),
				Generation: rand.Int64(),
				Annotations: map[string]string{
					keepKey:                                 keepValue,
					GatewayFrontendMTLSConfigMapsAnnotation: "old-ns/old-ca=" + fake.UUID().V4(),
					GatewayFrontendMTLSReferenceGrantsAnnotation: "old-ns/old-grant=" + fake.UUID().V4(),
				},
			},
			Status: gatewayv1.GatewayStatus{Conditions: []metav1.Condition{}},
		}
		wantAnnotations := map[string]string{
			keepKey:                              keepValue,
			GatewayProgrammingRevisionAnnotation: newRevision,
		}

		params := setConditionParams{
			resource:      gateway,
			conditions:    &gateway.Status.Conditions,
			conditionType: string(gatewayv1.GatewayConditionProgrammed),
			status:        metav1.ConditionTrue,
			reason:        string(gatewayv1.GatewayReasonProgrammed),
			message:       fake.Lorem().Sentence(10),
			annotations: map[string]string{
				GatewayProgrammingRevisionAnnotation: newRevision,
			},
			removeAnnotations: []string{
				GatewayFrontendMTLSConfigMapsAnnotation,
				GatewayFrontendMTLSReferenceGrantsAnnotation,
			},
		}

		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		statusUpdateCall := mockStatusWriter.EXPECT().
			Update(t.Context(), gateway, mock.Anything).
			Return(nil).
			Once()

		mockClient.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				gotGateway, ok := obj.(*gatewayv1.Gateway)
				require.True(t, ok)
				assert.Equal(t, wantAnnotations, gotGateway.GetAnnotations())
				return true
			}), mock.Anything).
			Return(nil).
			Once().
			NotBefore(statusUpdateCall)

		err := model.setCondition(t.Context(), params)
		require.NoError(t, err)
	})

	t.Run("HappyPath_AddsAnnotations_NoInitial", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		newAnnotations := map[string]string{
			"keyA": fake.Lorem().Sentence(10),
			"keyB": fake.Lorem().Sentence(10),
		}

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        fake.Internet().Domain(),
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
			conditionType: fake.Internet().Domain(),
			status:        metav1.ConditionTrue,
			reason:        fake.Lorem().Word(),
			message:       fake.Lorem().Sentence(10),
			annotations:   newAnnotations,
		}

		updateStatusCall := mockStatusWriter.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				gc, ok := obj.(*gatewayv1.GatewayClass)
				require.True(t, ok)
				require.Len(t, gc.Status.Conditions, 1)
				return true
			}), mock.Anything).
			Return(nil).
			Once()

		mockClient.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				gc, ok := obj.(*gatewayv1.GatewayClass)
				require.True(t, ok)
				assert.Equal(t, newAnnotations, gc.GetAnnotations(), "Annotations should match the new ones")
				return true
			}), mock.Anything).Return(nil).Once().NotBefore(updateStatusCall)

		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()

		err := model.setCondition(t.Context(), params)
		require.NoError(t, err)
	})

	t.Run("ErrorPath_AnnotationUpdateFails", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        fake.Internet().Domain(),
				Generation:  rand.Int64(),
				Annotations: map[string]string{"initial": fake.Lorem().Word()},
			},
			Spec:   gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
			Status: gatewayv1.GatewayClassStatus{Conditions: []metav1.Condition{}},
		}

		params := setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: fake.Internet().Domain(),
			status:        metav1.ConditionTrue,
			reason:        fake.Lorem().Word(),
			message:       fake.Lorem().Sentence(10),
			annotations:   map[string]string{"new": fake.Lorem().Word()},
		}

		expectedError := errors.New(fake.Lorem().Sentence(10))

		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)
		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.Anything, mock.Anything).
			Return(nil).
			Once()

		mockClient.EXPECT().
			Update(t.Context(), gatewayClass, mock.Anything).
			Return(expectedError).Once()

		err := model.setCondition(t.Context(), params)

		require.Error(t, err, "Expected an error from setCondition due to Update failure")
		require.ErrorIs(t, err, expectedError, "Returned error should wrap the original Update error")
	})

	t.Run("StatusUpdateConflict_RefreshesAndUpdatesStatus", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "gateway-class-" + fake.Lorem().Word(),
				Generation:      rand.Int64N(1000) + 1,
				ResourceVersion: "old",
			},
			Status: gatewayv1.GatewayClassStatus{Conditions: []metav1.Condition{}},
		}
		latestGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:            gatewayClass.Name,
				Generation:      gatewayClass.Generation,
				ResourceVersion: "latest",
			},
			Status: gatewayv1.GatewayClassStatus{Conditions: []metav1.Condition{{
				Type:               string(gatewayv1.GatewayClassConditionStatusAccepted),
				Status:             metav1.ConditionUnknown,
				Reason:             string(gatewayv1.GatewayClassReasonPending),
				ObservedGeneration: gatewayClass.Generation,
			}}},
		}
		conflictErr := apierrors.NewConflict(
			schema.GroupResource{Resource: "gatewayclasses"},
			gatewayClass.Name,
			errors.New("modified"),
		)

		mockClient.EXPECT().Status().Return(mockStatusWriter).Twice()
		mockStatusWriter.EXPECT().
			Update(t.Context(), gatewayClass, mock.Anything).
			Return(conflictErr).
			Once()
		getCall := mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Name: gatewayClass.Name}, mock.AnythingOfType("*v1.GatewayClass")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				*obj.(*gatewayv1.GatewayClass) = *latestGatewayClass
				return nil
			}).
			Once()
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
				updated := obj.(*gatewayv1.GatewayClass)
				condition := meta.FindStatusCondition(
					updated.Status.Conditions,
					string(gatewayv1.GatewayClassConditionStatusAccepted),
				)
				return updated.ResourceVersion == "latest" &&
					condition != nil &&
					condition.Status == metav1.ConditionTrue &&
					condition.Reason == string(gatewayv1.GatewayClassReasonAccepted)
			}), mock.Anything).
			Return(nil).
			Once().
			NotBefore(getCall)

		err := model.setCondition(t.Context(), setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: string(gatewayv1.GatewayClassConditionStatusAccepted),
			status:        metav1.ConditionTrue,
			reason:        string(gatewayv1.GatewayClassReasonAccepted),
			message:       "accepted",
		})

		require.NoError(t, err)
	})

	t.Run("StatusUpdateConflict_ReturnsRetryUpdateError", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway-class-" + fake.Lorem().Word()},
		}
		conflictErr := apierrors.NewConflict(
			schema.GroupResource{Resource: "gatewayclasses"},
			gatewayClass.Name,
			errors.New("modified"),
		)
		updateErr := errors.New("retry update failed")

		mockClient.EXPECT().Status().Return(mockStatusWriter).Twice()
		mockStatusWriter.EXPECT().Update(t.Context(), gatewayClass, mock.Anything).Return(conflictErr).Once()
		getCall := mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Name: gatewayClass.Name}, mock.AnythingOfType("*v1.GatewayClass")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				*obj.(*gatewayv1.GatewayClass) = gatewayv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{Name: gatewayClass.Name},
				}
				return nil
			}).
			Once()
		mockStatusWriter.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.GatewayClass"), mock.Anything).
			Return(updateErr).
			Once().
			NotBefore(getCall)

		err := model.setCondition(t.Context(), setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: string(gatewayv1.GatewayClassConditionStatusAccepted),
			status:        metav1.ConditionTrue,
			reason:        string(gatewayv1.GatewayClassReasonAccepted),
			message:       "accepted",
		})

		require.ErrorIs(t, err, updateErr)
		require.ErrorContains(t, err, "failed to update status")
	})

	t.Run("StatusUpdateConflict_ReturnsRefreshError", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway-class-" + fake.Lorem().Word()},
		}
		conflictErr := apierrors.NewConflict(
			schema.GroupResource{Resource: "gatewayclasses"},
			gatewayClass.Name,
			errors.New("modified"),
		)
		wantErr := errors.New("refresh failed")

		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		mockStatusWriter.EXPECT().Update(t.Context(), gatewayClass, mock.Anything).Return(conflictErr).Once()
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Name: gatewayClass.Name}, mock.AnythingOfType("*v1.GatewayClass")).
			Return(wantErr).
			Once()

		err := model.setCondition(t.Context(), setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: string(gatewayv1.GatewayClassConditionStatusAccepted),
			status:        metav1.ConditionTrue,
			reason:        string(gatewayv1.GatewayClassReasonAccepted),
			message:       "accepted",
		})

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to refresh resource")
	})

	t.Run("StatusUpdateConflict_ReturnsParentResolutionError", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)

		targetConditions := []metav1.Condition{{Type: string(gatewayv1.RouteConditionResolvedRefs)}}
		route := &gatewayv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "routes-" + fake.Lorem().Word(),
				Name:      "route-" + fake.Lorem().Word(),
			},
			Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{
				Parents: []gatewayv1.RouteParentStatus{{
					ParentRef:      gatewayv1.ParentReference{Name: "edge"},
					ControllerName: gatewayv1.GatewayController(ControllerClassName),
					Conditions:     targetConditions,
				}},
			}},
		}
		latestRoute := route.DeepCopy()
		latestRoute.Status.Parents = []gatewayv1.RouteParentStatus{
			{
				ParentRef:      gatewayv1.ParentReference{Name: "other"},
				ControllerName: gatewayv1.GatewayController(ControllerClassName),
			},
			{
				ParentRef:      gatewayv1.ParentReference{Name: "another"},
				ControllerName: gatewayv1.GatewayController(ControllerClassName),
			},
		}
		mockClient.EXPECT().
			Get(t.Context(), client.ObjectKeyFromObject(route), mock.AnythingOfType("*v1.HTTPRoute")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				*obj.(*gatewayv1.HTTPRoute) = *latestRoute
				return nil
			}).
			Once()

		err := model.updateResourceStatusAfterConflict(t.Context(), setConditionParams{
			resource:   route,
			conditions: &route.Status.Parents[0].Conditions,
		}, metav1.Condition{
			Type:   string(gatewayv1.RouteConditionResolvedRefs),
			Status: metav1.ConditionTrue,
			Reason: string(gatewayv1.RouteReasonResolvedRefs),
		})

		require.ErrorContains(t, err, "failed to resolve route parent status")
	})

	t.Run("HappyPath_AddsFinalizer_NoAnnotations", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		finalizerName := "test-finalizer/" + fake.Lorem().Word()

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:       fake.Internet().Domain(),
				Generation: rand.Int64(),
			},
			Spec:   gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
			Status: gatewayv1.GatewayClassStatus{Conditions: []metav1.Condition{}},
		}

		params := setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: fake.Internet().Domain(),
			status:        metav1.ConditionTrue,
			reason:        fake.Lorem().Word(),
			message:       fake.Lorem().Sentence(10),
			finalizer:     finalizerName,
		}

		// Mock status update
		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		mockStatusWriter.EXPECT().Update(t.Context(), gatewayClass, mock.Anything).Return(nil).Once()

		// Mock resource update (for finalizer)
		mockClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
			gc, ok := obj.(*gatewayv1.GatewayClass)
			require.True(t, ok)
			assert.Contains(t, gc.GetFinalizers(), finalizerName, "Finalizer should be added")
			return true
		}), mock.Anything).Return(nil).Once()

		err := model.setCondition(t.Context(), params)
		require.NoError(t, err)
	})

	t.Run("HappyPath_AddsFinalizer_AndAnnotations_SingleResourceUpdate", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		finalizerName := "test-finalizer/" + fake.Lorem().Word()
		newAnnotations := map[string]string{"newKey": fake.Lorem().Sentence(10)}
		initialAnnotations := map[string]string{"initialKey": fake.Lorem().Sentence(10)}
		expectedMergedAnnotations := map[string]string{
			"initialKey": initialAnnotations["initialKey"],
			"newKey":     newAnnotations["newKey"],
		}

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        fake.Internet().Domain(),
				Generation:  rand.Int64(),
				Annotations: initialAnnotations,
			},
			Spec:   gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
			Status: gatewayv1.GatewayClassStatus{Conditions: []metav1.Condition{}},
		}

		params := setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: fake.Internet().Domain(),
			status:        metav1.ConditionTrue,
			reason:        fake.Lorem().Word(),
			message:       fake.Lorem().Sentence(10),
			annotations:   newAnnotations,
			finalizer:     finalizerName,
		}

		// Mock status update
		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		statusUpdateCall := mockStatusWriter.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
			gc, ok := obj.(*gatewayv1.GatewayClass)
			require.True(t, ok)
			// Annotations should NOT be updated yet, only status conditions
			assert.Equal(t, initialAnnotations, gc.GetAnnotations())
			return true
		}), mock.Anything).Return(nil).Once()

		// Mock resource update (for both finalizer and annotations)
		// This should be called only ONCE
		mockClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
			gc, ok := obj.(*gatewayv1.GatewayClass)
			require.True(t, ok)
			assert.Contains(t, gc.GetFinalizers(), finalizerName, "Finalizer should be added")
			assert.Equal(t, expectedMergedAnnotations, gc.GetAnnotations(), "Annotations should be merged")
			return true
		}), mock.Anything).Return(nil).Once().NotBefore(statusUpdateCall)

		err := model.setCondition(t.Context(), params)
		require.NoError(t, err)
	})

	t.Run("ResourceUpdateConflict_RefreshesAndUpdatesMetadata", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		finalizerName := "test-finalizer/" + fake.Lorem().Word()
		annotationKey := "annotation-" + fake.Lorem().Word()
		annotationValue := "value-" + fake.Lorem().Word()
		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "gateway-class-" + fake.Lorem().Word(),
				Generation: rand.Int64N(1000) + 1,
			},
			Status: gatewayv1.GatewayClassStatus{Conditions: []metav1.Condition{}},
		}
		latestGatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:            gatewayClass.Name,
				Generation:      gatewayClass.Generation,
				ResourceVersion: "latest",
			},
		}
		conflictErr := apierrors.NewConflict(
			schema.GroupResource{Resource: "gatewayclasses"},
			gatewayClass.Name,
			errors.New("modified"),
		)

		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		mockStatusWriter.EXPECT().Update(t.Context(), gatewayClass, mock.Anything).Return(nil).Once()
		conflictCall := mockClient.EXPECT().Update(t.Context(), gatewayClass, mock.Anything).Return(conflictErr).Once()
		getCall := mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Name: gatewayClass.Name}, mock.AnythingOfType("*v1.GatewayClass")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				*obj.(*gatewayv1.GatewayClass) = *latestGatewayClass
				return nil
			}).
			Once().
			NotBefore(conflictCall)
		mockClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
			updated := obj.(*gatewayv1.GatewayClass)
			if updated.ResourceVersion != "latest" {
				return false
			}
			assert.Contains(t, updated.Finalizers, finalizerName)
			assert.Equal(t, annotationValue, updated.Annotations[annotationKey])
			return true
		}), mock.Anything).Return(nil).Once().NotBefore(getCall)

		err := model.setCondition(t.Context(), setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: string(gatewayv1.GatewayClassConditionStatusAccepted),
			status:        metav1.ConditionTrue,
			reason:        string(gatewayv1.GatewayClassReasonAccepted),
			message:       "accepted",
			finalizer:     finalizerName,
			annotations:   map[string]string{annotationKey: annotationValue},
		})

		require.NoError(t, err)
	})

	t.Run("ResourceUpdateConflict_ReturnsRefreshErrors", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway-class-" + fake.Lorem().Word()},
		}
		conflictErr := apierrors.NewConflict(
			schema.GroupResource{Resource: "gatewayclasses"},
			gatewayClass.Name,
			errors.New("modified"),
		)
		wantErr := errors.New("refresh failed")

		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		mockStatusWriter.EXPECT().Update(t.Context(), gatewayClass, mock.Anything).Return(nil).Once()
		mockClient.EXPECT().Update(t.Context(), gatewayClass, mock.Anything).Return(conflictErr).Once()
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Name: gatewayClass.Name}, mock.AnythingOfType("*v1.GatewayClass")).
			Return(wantErr).
			Once()

		err := model.setCondition(t.Context(), setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: string(gatewayv1.GatewayClassConditionStatusAccepted),
			status:        metav1.ConditionTrue,
			reason:        string(gatewayv1.GatewayClassReasonAccepted),
			message:       "accepted",
			finalizer:     "test-finalizer/" + fake.Lorem().Word(),
		})

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to refresh resource")
	})

	t.Run("ResourceUpdateConflict_ReturnsRetryUpdateErrors", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: "gateway-class-" + fake.Lorem().Word()},
		}
		conflictErr := apierrors.NewConflict(
			schema.GroupResource{Resource: "gatewayclasses"},
			gatewayClass.Name,
			errors.New("modified"),
		)
		wantErr := errors.New("retry update failed")

		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		mockStatusWriter.EXPECT().Update(t.Context(), gatewayClass, mock.Anything).Return(nil).Once()
		mockClient.EXPECT().Update(t.Context(), gatewayClass, mock.Anything).Return(conflictErr).Once()
		mockClient.EXPECT().
			Get(t.Context(), apitypes.NamespacedName{Name: gatewayClass.Name}, mock.AnythingOfType("*v1.GatewayClass")).
			RunAndReturn(func(_ context.Context, _ apitypes.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				*obj.(*gatewayv1.GatewayClass) = gatewayv1.GatewayClass{
					ObjectMeta: metav1.ObjectMeta{Name: gatewayClass.Name},
				}
				return nil
			}).
			Once()
		mockClient.EXPECT().
			Update(t.Context(), mock.AnythingOfType("*v1.GatewayClass"), mock.Anything).
			Return(wantErr).
			Once()

		err := model.setCondition(t.Context(), setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: string(gatewayv1.GatewayClassConditionStatusAccepted),
			status:        metav1.ConditionTrue,
			reason:        string(gatewayv1.GatewayClassReasonAccepted),
			message:       "accepted",
			finalizer:     "test-finalizer/" + fake.Lorem().Word(),
		})

		require.ErrorIs(t, err, wantErr)
		require.ErrorContains(t, err, "failed to update resource")
	})

	t.Run("ErrorPath_FinalizerUpdateFails", func(t *testing.T) {
		fake := faker.New()
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		finalizerName := "test-finalizer/" + fake.Lorem().Word()
		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{Name: fake.Internet().Domain(), Generation: rand.Int64()},
			Spec:       gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
			Status:     gatewayv1.GatewayClassStatus{Conditions: []metav1.Condition{}},
		}

		params := setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: fake.Internet().Domain(),
			status:        metav1.ConditionTrue,
			reason:        fake.Lorem().Word(),
			message:       fake.Lorem().Sentence(10),
			finalizer:     finalizerName,
		}

		expectedError := errors.New("failed to update resource with finalizer")

		// Mock status update
		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		mockStatusWriter.EXPECT().Update(t.Context(), gatewayClass, mock.Anything).Return(nil).Once()

		// Mock resource update to fail
		mockClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
			gc, ok := obj.(*gatewayv1.GatewayClass)
			require.True(t, ok)
			// controllerutil.AddFinalizer would have added it in memory already
			assert.Contains(t, gc.GetFinalizers(), finalizerName)
			return true
		}), mock.Anything).Return(expectedError).Once()

		err := model.setCondition(t.Context(), params)
		require.Error(t, err)
		require.ErrorIs(t, err, expectedError)
	})

	t.Run("HappyPath_NoFinalizer_AnnotationsAdded_ResourceUpdateOccurs", func(t *testing.T) {
		fake := faker.New()

		// This test is to ensure that if only annotations are provided (no finalizer),
		// the resource update for annotations still occurs.
		deps := newMockDeps(t)
		model := newResourcesModel(deps)
		mockClient, _ := deps.K8sClient.(*Mockk8sClient)
		mockStatusWriter := k8sapi.NewMockSubResourceWriter(t)

		newAnnotations := map[string]string{"newKey": fake.Lorem().Sentence(10)}

		gatewayClass := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:       fake.Internet().Domain(),
				Generation: rand.Int64(),
			},
			Spec:   gatewayv1.GatewayClassSpec{ControllerName: ControllerClassName},
			Status: gatewayv1.GatewayClassStatus{Conditions: []metav1.Condition{}},
		}

		params := setConditionParams{
			resource:      gatewayClass,
			conditions:    &gatewayClass.Status.Conditions,
			conditionType: fake.Internet().Domain(),
			status:        metav1.ConditionTrue,
			reason:        fake.Lorem().Word(),
			message:       fake.Lorem().Sentence(10),
			annotations:   newAnnotations,
			// finalizer is empty
		}

		mockClient.EXPECT().Status().Return(mockStatusWriter).Once()
		statusUpdateCall := mockStatusWriter.EXPECT().
			Update(t.Context(), gatewayClass, mock.Anything).
			Return(nil).
			Once()

		mockClient.EXPECT().Update(t.Context(), mock.MatchedBy(func(obj client.Object) bool {
			gc, ok := obj.(*gatewayv1.GatewayClass)
			require.True(t, ok)
			assert.Equal(t, newAnnotations, gc.GetAnnotations())
			return true
		}), mock.Anything).Return(nil).Once().NotBefore(statusUpdateCall)

		err := model.setCondition(t.Context(), params)
		require.NoError(t, err)
	})
}

func TestResourcesModelImpl_statusConditionsForRetry(t *testing.T) {
	fake := faker.New()

	t.Run("returns top level Gateway conditions", func(t *testing.T) {
		gateway := &gatewayv1.Gateway{Status: gatewayv1.GatewayStatus{Conditions: []metav1.Condition{{
			Type:   "Programmed",
			Status: metav1.ConditionUnknown,
			Reason: "Pending-" + fake.Lorem().Word(),
		}}}}

		conditions, err := statusConditionsForRetry(gateway, gateway, nil)

		require.NoError(t, err)
		require.Same(t, &gateway.Status.Conditions[0], &(*conditions)[0])
	})

	t.Run("returns top level ListenerSet conditions", func(t *testing.T) {
		listenerSet := &gatewayv1.ListenerSet{Status: gatewayv1.ListenerSetStatus{Conditions: []metav1.Condition{{
			Type:   "Accepted",
			Status: metav1.ConditionUnknown,
			Reason: "Pending-" + fake.Lorem().Word(),
		}}}}

		conditions, err := statusConditionsForRetry(listenerSet, listenerSet, nil)

		require.NoError(t, err)
		require.Same(t, &listenerSet.Status.Conditions[0], &(*conditions)[0])
	})

	t.Run("returns matching HTTPRoute parent conditions", func(t *testing.T) {
		targetConditions := []metav1.Condition{{
			Type:   string(gatewayv1.RouteConditionResolvedRefs),
			Status: metav1.ConditionTrue,
			Reason: string(gatewayv1.RouteReasonResolvedRefs),
		}}
		otherParentName := gatewayv1.ObjectName("other-" + fake.Lorem().Word())
		edgeParentName := gatewayv1.ObjectName("edge-" + fake.Lorem().Word())
		original := &gatewayv1.HTTPRoute{Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{
			Parents: []gatewayv1.RouteParentStatus{
				{
					ParentRef:      gatewayv1.ParentReference{Name: otherParentName},
					ControllerName: gatewayv1.GatewayController(ControllerClassName),
					Conditions:     []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue}},
				},
				{
					ParentRef:      gatewayv1.ParentReference{Name: edgeParentName},
					ControllerName: gatewayv1.GatewayController(ControllerClassName),
					Conditions:     targetConditions,
				},
			},
		}}}
		latest := original.DeepCopy()
		latest.Status.Parents[1].Conditions = []metav1.Condition{{
			Type:   string(gatewayv1.RouteConditionResolvedRefs),
			Status: metav1.ConditionUnknown,
			Reason: string(gatewayv1.RouteReasonPending),
		}}

		conditions, err := statusConditionsForRetry(latest, original, targetConditions)

		require.NoError(t, err)
		require.Same(t, &latest.Status.Parents[1].Conditions[0], &(*conditions)[0])
	})

	t.Run("returns matching GRPCRoute parent conditions", func(t *testing.T) {
		targetConditions := []metav1.Condition{{
			Type:   string(gatewayv1.RouteConditionAccepted),
			Status: metav1.ConditionFalse,
			Reason: string(gatewayv1.RouteReasonAccepted),
		}}
		parentName := gatewayv1.ObjectName("grpc-" + fake.Lorem().Word())
		original := &gatewayv1.GRPCRoute{Status: gatewayv1.GRPCRouteStatus{RouteStatus: gatewayv1.RouteStatus{
			Parents: []gatewayv1.RouteParentStatus{{
				ParentRef:      gatewayv1.ParentReference{Name: parentName},
				ControllerName: gatewayv1.GatewayController(ControllerClassName),
				Conditions:     targetConditions,
			}},
		}}}
		latest := original.DeepCopy()

		conditions, err := statusConditionsForRetry(latest, original, targetConditions)

		require.NoError(t, err)
		require.Same(t, &latest.Status.Parents[0].Conditions[0], &(*conditions)[0])
	})

	t.Run("returns error for mismatched route original type", func(t *testing.T) {
		latest := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "route-" + fake.Lorem().Word()}}
		original := &gatewayv1.Gateway{ObjectMeta: metav1.ObjectMeta{Name: "gateway-" + fake.Lorem().Word()}}

		conditions, err := statusConditionsForRetry(latest, original, nil)

		require.Nil(t, conditions)
		require.ErrorContains(t, err, "failed to resolve status conditions")
	})

	t.Run("returns error when route parent is not found", func(t *testing.T) {
		targetConditions := []metav1.Condition{{Type: string(gatewayv1.RouteConditionAccepted)}}
		edgeParentName := gatewayv1.ObjectName("edge-" + fake.Lorem().Word())
		otherParentName := gatewayv1.ObjectName("other-" + fake.Lorem().Word())
		anotherParentName := gatewayv1.ObjectName("another-" + fake.Lorem().Word())
		original := &gatewayv1.HTTPRoute{Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{
			Parents: []gatewayv1.RouteParentStatus{{
				ParentRef:      gatewayv1.ParentReference{Name: edgeParentName},
				ControllerName: gatewayv1.GatewayController(ControllerClassName),
				Conditions:     targetConditions,
			}},
		}}}
		latest := original.DeepCopy()
		latest.Status.Parents[0].ParentRef.Name = otherParentName
		latest.Status.Parents = append(latest.Status.Parents, gatewayv1.RouteParentStatus{
			ParentRef:      gatewayv1.ParentReference{Name: anotherParentName},
			ControllerName: gatewayv1.GatewayController(ControllerClassName),
		})

		conditions, err := statusConditionsForRetry(latest, original, targetConditions)

		require.Nil(t, conditions)
		require.ErrorContains(t, err, "failed to resolve route parent status")
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
		fake := faker.New()
		resource := &gatewayv1.GatewayClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:       fake.Internet().Domain(), // Use faker for name
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
		fake := faker.New()
		condition := metav1.Condition{
			Type:               fake.Internet().Domain(),
			Status:             metav1.ConditionTrue,
			Reason:             fake.Lorem().Word(),
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
	randomConditionWithStatus := func(status metav1.ConditionStatus) randomConditionsOpt {
		return func(condition *metav1.Condition) {
			condition.Status = status
		}
	}

	t.Run("ConditionSetAndMatches", func(t *testing.T) {
		fake := faker.New()
		model := newResourcesModel(newMockDeps(t))
		conditionType := fake.Internet().Domain()
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
		fake := faker.New()
		model := newResourcesModel(newMockDeps(t))
		conditionType := fake.Internet().Domain()
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
		fake := faker.New()
		model := newResourcesModel(newMockDeps(t))
		conditionType := fake.Internet().Domain()
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
		fake := faker.New()
		model := newResourcesModel(newMockDeps(t))
		conditionType := fake.Internet().Domain()
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

	t.Run("ConditionSet_CurrentGenerationButNotTrue", func(t *testing.T) {
		fake := faker.New()
		model := newResourcesModel(newMockDeps(t))
		conditionType := fake.Internet().Domain()
		generation := rand.Int64()

		for _, status := range []metav1.ConditionStatus{
			metav1.ConditionFalse,
			metav1.ConditionUnknown,
		} {
			t.Run(string(status), func(t *testing.T) {
				gatewayClass := newRandomResource(
					randomResourceWithGeneration(generation),
					randomResourceWithConditions(
						newRandomConditions(
							randomConditionWithType(conditionType),
							randomConditionWithObservedGeneration(generation),
							randomConditionWithStatus(status),
						),
					),
				)

				result := model.isConditionSet(isConditionSetParams{
					resource:      gatewayClass,
					conditions:    gatewayClass.Status.Conditions,
					conditionType: conditionType,
				})

				assert.False(t, result, "Expected false for current generation condition that is not True")
			})
		}
	})

	t.Run("ConditionSetAndMatches_WithMatchingAnnotations", func(t *testing.T) {
		fake := faker.New()
		model := newResourcesModel(newMockDeps(t))
		conditionType := fake.Internet().Domain()
		generation := rand.Int64()

		key1 := "key1-" + fake.Lorem().Word()
		key2 := "key2-" + fake.Lorem().Word()
		val1 := fake.Lorem().Sentence(10)
		val2 := fake.Lorem().Sentence(10)

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
		fake := faker.New()
		model := newResourcesModel(newMockDeps(t))
		conditionType := fake.Internet().Domain()
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
				"key1-" + fake.Lorem().Word(): fake.Lorem().Sentence(10),
				"key2-" + fake.Lorem().Word(): fake.Lorem().Sentence(10),
			},
		}
		result := model.isConditionSet(params)
		assert.False(t, result, "Expected false when a requested annotation value mismatches")
	})

	t.Run("ConditionSetAndMatches_WithMismatchedAnnotationValue", func(t *testing.T) {
		fake := faker.New()
		model := newResourcesModel(newMockDeps(t))
		conditionType := fake.Internet().Domain()
		generation := rand.Int64()
		key := "key-" + fake.Lorem().Word()
		gatewayClass := newRandomResource(
			randomResourceWithGeneration(generation),
			randomResourceWithAnnotations(map[string]string{key: fake.Lorem().Sentence(10)}),
		)
		params := isConditionSetParams{
			resource:      gatewayClass,
			conditions:    gatewayClass.Status.Conditions,
			conditionType: conditionType,
			annotations:   map[string]string{key: "other-" + fake.Lorem().Sentence(10)},
		}
		result := model.isConditionSet(params)
		assert.False(t, result, "Expected false when a requested annotation value mismatches")
	})

	t.Run("ConditionSetAndMatches_WithExtraResourceAnnotation", func(t *testing.T) {
		fake := faker.New()
		model := newResourcesModel(newMockDeps(t))
		conditionType := fake.Internet().Domain()
		generation := rand.Int64()
		key := "key-" + fake.Lorem().Word()
		val := fake.Lorem().Sentence(10)
		gatewayClass := newRandomResource(
			randomResourceWithGeneration(generation),
			randomResourceWithAnnotations(map[string]string{
				key:      val,
				"extra1": fake.Lorem().Sentence(10),
				"extra2": fake.Lorem().Sentence(10),
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
