package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"

	"go.uber.org/dig"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type setConditionParams struct {
	resource          client.Object
	conditions        *[]metav1.Condition
	conditionType     string
	status            metav1.ConditionStatus
	reason            string
	message           string
	annotations       map[string]string
	removeAnnotations []string
	finalizer         string
}

type isConditionSetParams struct {
	resource      client.Object
	conditions    []metav1.Condition
	conditionType string
	annotations   map[string]string
}

type resourcesModel interface {
	// setCondition sets a condition on a given resource.
	setCondition(ctx context.Context, params setConditionParams) error

	// isConditionSet checks if a specific condition is already set, true, and observed at the correct generation.
	isConditionSet(params isConditionSetParams) bool
}

type resourcesModelImpl struct {
	client k8sClient
	logger *slog.Logger
}

func (m *resourcesModelImpl) setCondition(ctx context.Context, params setConditionParams) error {
	generation := params.resource.GetGeneration()
	m.logger.DebugContext(ctx,
		fmt.Sprintf("Setting %s condition", params.conditionType),
		slog.String("resource", params.resource.GetName()),
		slog.String("status", string(params.status)),
		slog.String("reason", params.reason),
		slog.String("message", params.message),
		slog.Any("annotations", params.annotations),
		slog.String("finalizer", params.finalizer),
		slog.Int64("generation", generation),
		slog.String("resourceVersion", params.resource.GetResourceVersion()),
	)

	acceptedCondition := metav1.Condition{
		Type:               params.conditionType,
		Status:             params.status,
		Reason:             params.reason,
		Message:            params.message,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}

	meta.SetStatusCondition(params.conditions, acceptedCondition)

	if err := m.client.Status().Update(ctx, params.resource); err != nil {
		if !apierrors.IsConflict(err) {
			return fmt.Errorf("failed to update status for %s: %w", params.resource.GetName(), err)
		}
		if retryErr := m.updateResourceStatusAfterConflict(ctx, params, acceptedCondition); retryErr != nil {
			return retryErr
		}
	}

	needsResourceUpdate := false
	if params.finalizer != "" {
		needsResourceUpdate = controllerutil.AddFinalizer(params.resource, params.finalizer)
	}

	if len(params.annotations) > 0 || len(params.removeAnnotations) > 0 {
		currentAnnotations := params.resource.GetAnnotations()
		if currentAnnotations == nil {
			currentAnnotations = make(map[string]string)
		}
		for _, key := range params.removeAnnotations {
			delete(currentAnnotations, key)
		}
		maps.Copy(currentAnnotations, params.annotations)
		params.resource.SetAnnotations(currentAnnotations)
		needsResourceUpdate = true
	}

	if needsResourceUpdate {
		if err := m.client.Update(ctx, params.resource); err != nil {
			if apierrors.IsConflict(err) {
				return m.updateResourceMetadataAfterConflict(ctx, params)
			}
			return fmt.Errorf(
				"failed to update resource %s with finalizer/annotations: %w",
				params.resource.GetName(),
				err,
			)
		}
	}

	return nil
}

func (m *resourcesModelImpl) updateResourceStatusAfterConflict(
	ctx context.Context,
	params setConditionParams,
	condition metav1.Condition,
) error {
	latest, ok := params.resource.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("failed to copy resource %s for status update", params.resource.GetName())
	}
	if err := m.client.Get(ctx, client.ObjectKeyFromObject(params.resource), latest); err != nil {
		return fmt.Errorf(
			"failed to refresh resource %s after status conflict: %w",
			params.resource.GetName(),
			err,
		)
	}
	conditions, err := statusConditionsForRetry(latest, params.resource, *params.conditions)
	if err != nil {
		return err
	}
	meta.SetStatusCondition(conditions, condition)
	if updateErr := m.client.Status().Update(ctx, latest); updateErr != nil {
		return fmt.Errorf("failed to update status for %s after conflict: %w", params.resource.GetName(), updateErr)
	}
	return nil
}

func statusConditionsForRetry(
	latest client.Object,
	original client.Object,
	targetConditions []metav1.Condition,
) (*[]metav1.Condition, error) {
	switch latestResource := latest.(type) {
	case *gatewayv1.GatewayClass:
		return &latestResource.Status.Conditions, nil
	case *gatewayv1.Gateway:
		return &latestResource.Status.Conditions, nil
	case *gatewayv1.ListenerSet:
		return &latestResource.Status.Conditions, nil
	case *gatewayv1.HTTPRoute:
		originalRoute, ok := original.(*gatewayv1.HTTPRoute)
		if !ok {
			break
		}
		return routeParentConditionsForRetry(
			latestResource.Status.Parents,
			originalRoute.Status.Parents,
			targetConditions,
		)
	case *gatewayv1.GRPCRoute:
		originalRoute, ok := original.(*gatewayv1.GRPCRoute)
		if !ok {
			break
		}
		return routeParentConditionsForRetry(
			latestResource.Status.Parents,
			originalRoute.Status.Parents,
			targetConditions,
		)
	}
	return nil, fmt.Errorf("failed to resolve status conditions for %s after conflict", original.GetName())
}

func routeParentConditionsForRetry(
	latestParents []gatewayv1.RouteParentStatus,
	originalParents []gatewayv1.RouteParentStatus,
	targetConditions []metav1.Condition,
) (*[]metav1.Condition, error) {
	if len(originalParents) == 1 && len(latestParents) == 1 {
		return &latestParents[0].Conditions, nil
	}
	for _, originalParent := range originalParents {
		if !reflect.DeepEqual(originalParent.Conditions, targetConditions) {
			continue
		}
		for i := range latestParents {
			latestParent := &latestParents[i]
			if latestParent.ControllerName == originalParent.ControllerName &&
				parentRefSameTarget(latestParent.ParentRef, originalParent.ParentRef) {
				return &latestParent.Conditions, nil
			}
		}
	}
	return nil, errors.New("failed to resolve route parent status after conflict")
}

func (m *resourcesModelImpl) updateResourceMetadataAfterConflict(
	ctx context.Context,
	params setConditionParams,
) error {
	latest, ok := params.resource.DeepCopyObject().(client.Object)
	if !ok {
		return fmt.Errorf("failed to copy resource %s for metadata update", params.resource.GetName())
	}
	if err := m.client.Get(ctx, client.ObjectKeyFromObject(params.resource), latest); err != nil {
		return fmt.Errorf(
			"failed to refresh resource %s after finalizer/annotations conflict: %w",
			params.resource.GetName(),
			err,
		)
	}
	if params.finalizer != "" {
		controllerutil.AddFinalizer(latest, params.finalizer)
	}
	if len(params.annotations) > 0 {
		currentAnnotations := latest.GetAnnotations()
		if currentAnnotations == nil {
			currentAnnotations = make(map[string]string)
		}
		maps.Copy(currentAnnotations, params.annotations)
		latest.SetAnnotations(currentAnnotations)
	}
	if err := m.client.Update(ctx, latest); err != nil {
		return fmt.Errorf(
			"failed to update resource %s with finalizer/annotations after conflict: %w",
			params.resource.GetName(),
			err,
		)
	}
	return nil
}

func (m *resourcesModelImpl) isConditionSet(params isConditionSetParams) bool {
	if len(params.annotations) > 0 {
		resourceAnnotations := params.resource.GetAnnotations()
		if resourceAnnotations == nil {
			return false
		}
		for key, expectedValue := range params.annotations {
			actualValue, found := resourceAnnotations[key]
			if !found || actualValue != expectedValue {
				return false
			}
		}
	}

	existingCondition := meta.FindStatusCondition(params.conditions, params.conditionType)
	if existingCondition != nil &&
		existingCondition.Status == metav1.ConditionTrue &&
		existingCondition.ObservedGeneration == params.resource.GetGeneration() {
		return true
	}
	return false
}

type resourcesModelDeps struct {
	dig.In

	K8sClient  k8sClient
	RootLogger *slog.Logger
}

func newResourcesModel(deps resourcesModelDeps) *resourcesModelImpl {
	return &resourcesModelImpl{
		client: deps.K8sClient,
		logger: deps.RootLogger.WithGroup("resources-model"),
	}
}
