package app

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/dig"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type setConditionParams struct {
	resource      client.Object
	conditions    *[]metav1.Condition
	conditionType string
	status        metav1.ConditionStatus
	reason        string
	message       string
	annotations   map[string]string
	finalizer     string
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
		return fmt.Errorf("failed to update status for %s: %w", params.resource.GetName(), err)
	}

	needsResourceUpdate := false
	if params.finalizer != "" {
		needsResourceUpdate = controllerutil.AddFinalizer(params.resource, params.finalizer)
	}

	if len(params.annotations) > 0 {
		currentAnnotations := params.resource.GetAnnotations()
		if currentAnnotations == nil {
			currentAnnotations = make(map[string]string)
		}
		for k, v := range params.annotations {
			currentAnnotations[k] = v
		}
		params.resource.SetAnnotations(currentAnnotations)
		needsResourceUpdate = true
	}

	if needsResourceUpdate {
		if err := m.client.Update(ctx, params.resource); err != nil {
			return fmt.Errorf("failed to update resource %s with finalizer/annotations: %w", params.resource.GetName(), err)
		}
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

func newResourcesModel(deps resourcesModelDeps) resourcesModel {
	return &resourcesModelImpl{
		client: deps.K8sClient,
		logger: deps.RootLogger.WithGroup("resources-model"),
	}
}
