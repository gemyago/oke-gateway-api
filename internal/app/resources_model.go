package app

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/dig"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type setConditionParams struct {
	resource      client.Object
	conditions    *[]metav1.Condition
	conditionType string
	status        metav1.ConditionStatus
	reason        string
	message       string
}

type resourcesModel interface {
	// setCondition sets a condition on a given resource.
	setCondition(ctx context.Context, params setConditionParams) error

	// isConditionSet checks if a specific condition is already set, true, and observed at the correct generation.
	isConditionSet(resource client.Object, conditions []metav1.Condition, conditionType string) bool
}

type resourcesModelImpl struct {
	client k8sClient
	logger *slog.Logger
}

func (m *resourcesModelImpl) setCondition(ctx context.Context, params setConditionParams) error {
	m.logger.DebugContext(ctx,
		fmt.Sprintf("Setting %s condition", params.conditionType),
		slog.String("resource", params.resource.GetName()),
		slog.String("status", string(params.status)),
		slog.String("reason", params.reason),
		slog.String("message", params.message),
	)

	generation := params.resource.GetGeneration()

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

	return nil
}

func (m *resourcesModelImpl) isConditionSet(
	resource client.Object,
	conditions []metav1.Condition,
	conditionType string) bool {
	existingCondition := meta.FindStatusCondition(conditions, conditionType)
	if existingCondition != nil &&
		existingCondition.ObservedGeneration == resource.GetGeneration() {
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
