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

type setAcceptedConditionParams struct {
	resource   client.Object
	conditions *[]metav1.Condition
	message    string
}

type setNotAcceptedConditionParams struct {
	resource   client.Object
	conditions *[]metav1.Condition
	reason     string
	message    string
}

// ResourcesModel handles logic related to Kubernetes resource manipulation.
type resourcesModel interface {
	// SetAcceptedCondition sets the 'Accepted' status condition on a given resource.
	setAcceptedCondition(ctx context.Context, params setAcceptedConditionParams) error

	// SetNotAcceptedCondition sets the 'Accepted' status condition to 'False' on a given resource.
	setNotAcceptedCondition(ctx context.Context, params setNotAcceptedConditionParams) error

	// isConditionSet checks if a specific condition is already set, true, and observed at the correct generation.
	isConditionSet(resource client.Object, conditions []metav1.Condition, conditionType string) bool
}

type resourcesModelImpl struct {
	client k8sClient
	logger *slog.Logger
}

func (m *resourcesModelImpl) setAcceptedCondition(ctx context.Context, params setAcceptedConditionParams) error {
	m.logger.DebugContext(ctx,
		"Setting Accepted condition",
		slog.String("resource", params.resource.GetName()),
		slog.String("message", params.message),
	)

	generation := params.resource.GetGeneration()

	acceptedCondition := metav1.Condition{
		Type:               AcceptedConditionType,
		Status:             metav1.ConditionTrue,
		Reason:             AcceptedConditionReason,
		Message:            params.message,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}

	meta.SetStatusCondition(params.conditions, acceptedCondition)

	if err := m.client.Status().Update(ctx, params.resource); err != nil {
		return fmt.Errorf("failed to update GatewayClass status for %s: %w", params.resource.GetName(), err)
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

func (m *resourcesModelImpl) setNotAcceptedCondition(
	ctx context.Context,
	params setNotAcceptedConditionParams,
) error {
	m.logger.DebugContext(ctx,
		"Setting NotAccepted condition",
		slog.String("resource", params.resource.GetName()),
		slog.String("message", params.message),
		slog.String("reason", params.reason),
	)

	generation := params.resource.GetGeneration()

	notAcceptedCondition := metav1.Condition{
		Type:               AcceptedConditionType,
		Status:             metav1.ConditionFalse,
		Reason:             params.reason,
		Message:            params.message,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.Now(),
	}

	meta.SetStatusCondition(params.conditions, notAcceptedCondition)

	if err := m.client.Status().Update(ctx, params.resource); err != nil {
		return fmt.Errorf("failed to update resource status for %s: %w", params.resource.GetName(), err)
	}

	return nil
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
