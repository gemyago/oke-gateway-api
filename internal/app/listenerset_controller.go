package app

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"

	"go.uber.org/dig"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type listenerSetStatusClient interface {
	Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
	Status() client.SubResourceWriter
	Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error
}

// ListenerSetController reconciles ListenerSet status that cannot be driven by parent Gateway reconciliation.
type ListenerSetController struct {
	logger *slog.Logger
	client listenerSetStatusClient
}

type ListenerSetControllerDeps struct {
	dig.In

	RootLogger *slog.Logger
	K8sClient  k8sClient
}

func NewListenerSetController(deps ListenerSetControllerDeps) *ListenerSetController {
	return &ListenerSetController{
		logger: deps.RootLogger.WithGroup("listenerset-controller"),
		client: deps.K8sClient,
	}
}

func (c *ListenerSetController) Reconcile(
	ctx context.Context,
	req reconcile.Request,
) (reconcile.Result, error) {
	c.logger.InfoContext(ctx, fmt.Sprintf("Processing reconciliation for ListenerSet %s", req.NamespacedName))

	var listenerSet gatewayv1.ListenerSet
	if err := c.client.Get(ctx, req.NamespacedName, &listenerSet); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get ListenerSet %s: %w", req.NamespacedName, err)
	}
	if listenerSet.DeletionTimestamp != nil {
		return reconcile.Result{}, nil
	}

	status, err := c.statusForListenerSet(ctx, listenerSet)
	if err != nil {
		return reconcile.Result{}, err
	}
	if listenerSetStatusSemanticallyEqual(listenerSet.Status, status) {
		return reconcile.Result{}, c.updateParentGatewayAnnotation(ctx, req.NamespacedName, listenerSet)
	}
	listenerSet.Status = status
	if err = c.client.Status().Update(ctx, &listenerSet); err != nil {
		return reconcile.Result{}, fmt.Errorf(
			"failed to update ListenerSet %s status: %w",
			req.NamespacedName,
			err,
		)
	}
	if err = c.updateParentGatewayAnnotation(ctx, req.NamespacedName, listenerSet); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (c *ListenerSetController) updateParentGatewayAnnotation(
	ctx context.Context,
	listenerSetKey client.ObjectKey,
	resolvedListenerSet gatewayv1.ListenerSet,
) error {
	desiredParent := ""
	if parentName, ok := listenerSetParentGatewayName(resolvedListenerSet); ok {
		desiredParent = parentName
	}

	var listenerSet gatewayv1.ListenerSet
	if err := c.client.Get(ctx, listenerSetKey, &listenerSet); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to refresh ListenerSet %s before annotation update: %w", listenerSetKey, err)
	}

	annotations := listenerSet.Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}
	if annotations[ListenerSetParentGatewayAnnotation] == desiredParent {
		return nil
	}

	nextAnnotations := maps.Clone(annotations)
	if desiredParent == "" {
		delete(nextAnnotations, ListenerSetParentGatewayAnnotation)
	} else {
		nextAnnotations[ListenerSetParentGatewayAnnotation] = desiredParent
	}
	listenerSet.Annotations = nextAnnotations
	if err := c.client.Update(ctx, &listenerSet); err != nil {
		return fmt.Errorf(
			"failed to update ListenerSet %s/%s parent Gateway annotation: %w",
			listenerSet.Namespace,
			listenerSet.Name,
			err,
		)
	}
	return nil
}

func (c *ListenerSetController) statusForListenerSet(
	ctx context.Context,
	listenerSet gatewayv1.ListenerSet,
) (gatewayv1.ListenerSetStatus, error) {
	parentName, ok := listenerSetParentGatewayName(listenerSet)
	if !ok {
		return rejectedListenerSetStatus(
			listenerSet,
			gatewayv1.ListenerSetReasonInvalid,
			"ListenerSet parentRef must target a Gateway",
		), nil
	}
	parentNamespace, parentGatewayName, ok := stringsCutParentName(parentName)
	if !ok {
		return rejectedListenerSetStatus(
			listenerSet,
			gatewayv1.ListenerSetReasonInvalid,
			"ListenerSet parentRef is invalid",
		), nil
	}

	var gateway gatewayv1.Gateway
	if err := c.client.Get(ctx, apitypes.NamespacedName{
		Namespace: parentNamespace,
		Name:      parentGatewayName,
	}, &gateway); err != nil {
		if apierrors.IsNotFound(err) {
			return rejectedListenerSetStatus(
				listenerSet,
				gatewayv1.ListenerSetReasonParentNotAccepted,
				fmt.Sprintf("parent Gateway %s was not found", parentName),
			), nil
		}
		return gatewayv1.ListenerSetStatus{}, fmt.Errorf("failed to get parent Gateway %s: %w", parentName, err)
	}

	var gatewayClass gatewayv1.GatewayClass
	if err := c.client.Get(
		ctx,
		apitypes.NamespacedName{Name: string(gateway.Spec.GatewayClassName)},
		&gatewayClass,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return rejectedListenerSetStatus(
				listenerSet,
				gatewayv1.ListenerSetReasonParentNotAccepted,
				fmt.Sprintf("parent GatewayClass %s was not found", gateway.Spec.GatewayClassName),
			), nil
		}
		return gatewayv1.ListenerSetStatus{}, fmt.Errorf(
			"failed to get parent GatewayClass %s: %w",
			gateway.Spec.GatewayClassName,
			err,
		)
	}
	if !listenerSetGatewayClassSupported(gatewayClass) {
		return rejectedListenerSetStatus(
			listenerSet,
			gatewayv1.ListenerSetReasonParentNotAccepted,
			fmt.Sprintf("parent GatewayClass %s is not managed by this controller", gateway.Spec.GatewayClassName),
		), nil
	}

	var listenerSetNamespace corev1.Namespace
	if err := c.client.Get(
		ctx,
		apitypes.NamespacedName{Name: listenerSet.Namespace},
		&listenerSetNamespace,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return rejectedListenerSetStatus(
				listenerSet,
				gatewayv1.ListenerSetReasonNotAllowed,
				fmt.Sprintf("ListenerSet namespace %s was not found", listenerSet.Namespace),
			), nil
		}
		return gatewayv1.ListenerSetStatus{}, fmt.Errorf(
			"failed to get ListenerSet namespace %s: %w",
			listenerSet.Namespace,
			err,
		)
	}
	if !listenerSetAllowedByGateway(gateway, listenerSet, listenerSetNamespace) {
		return rejectedListenerSetStatus(
			listenerSet,
			gatewayv1.ListenerSetReasonNotAllowed,
			fmt.Sprintf(
				"ListenerSet %s/%s is not allowed by Gateway %s",
				listenerSet.Namespace,
				listenerSet.Name,
				parentName,
			),
		), nil
	}

	return pendingListenerSetStatus(listenerSet, gateway), nil
}

func listenerSetGatewayClassSupported(gatewayClass gatewayv1.GatewayClass) bool {
	return gatewayClass.Spec.ControllerName == gatewayv1.GatewayController(ControllerClassName) ||
		gatewayClass.Spec.ControllerName == gatewayv1.GatewayController(NetworkLoadBalancerControllerClassName)
}

func stringsCutParentName(parentName string) (string, string, bool) {
	namespace, name, ok := strings.Cut(parentName, "/")
	return namespace, name, ok
}

func pendingListenerSetStatus(
	listenerSet gatewayv1.ListenerSet,
	gateway gatewayv1.Gateway,
) gatewayv1.ListenerSetStatus {
	status := listenerSetStatusWithConditions(
		listenerSet,
		metav1.ConditionTrue,
		gatewayv1.ListenerSetReasonAccepted,
		fmt.Sprintf(
			"ListenerSet %s/%s accepted by Gateway %s/%s",
			listenerSet.Namespace,
			listenerSet.Name,
			gateway.Namespace,
			gateway.Name,
		),
		metav1.ConditionUnknown,
		gatewayv1.ListenerSetReasonPending,
		fmt.Sprintf(
			"ListenerSet %s/%s is waiting for parent Gateway programming",
			listenerSet.Namespace,
			listenerSet.Name,
		),
	)
	status.Listeners = listenerSetPendingListenerStatuses(listenerSet)
	return status
}

func rejectedListenerSetStatus(
	listenerSet gatewayv1.ListenerSet,
	reason gatewayv1.ListenerSetConditionReason,
	message string,
) gatewayv1.ListenerSetStatus {
	status := listenerSetStatusWithConditions(
		listenerSet,
		metav1.ConditionFalse,
		reason,
		message,
		metav1.ConditionFalse,
		reason,
		message,
	)
	status.Listeners = listenerSetPendingListenerStatuses(listenerSet)
	return status
}

func listenerSetStatusWithConditions(
	listenerSet gatewayv1.ListenerSet,
	acceptedStatus metav1.ConditionStatus,
	acceptedReason gatewayv1.ListenerSetConditionReason,
	acceptedMessage string,
	programmedStatus metav1.ConditionStatus,
	programmedReason gatewayv1.ListenerSetConditionReason,
	programmedMessage string,
) gatewayv1.ListenerSetStatus {
	status := gatewayv1.ListenerSetStatus{}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               string(gatewayv1.ListenerSetConditionAccepted),
		Status:             acceptedStatus,
		Reason:             string(acceptedReason),
		ObservedGeneration: listenerSet.Generation,
		LastTransitionTime: metav1.Now(),
		Message:            acceptedMessage,
	})
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               string(gatewayv1.ListenerSetConditionProgrammed),
		Status:             programmedStatus,
		Reason:             string(programmedReason),
		ObservedGeneration: listenerSet.Generation,
		LastTransitionTime: metav1.Now(),
		Message:            programmedMessage,
	})
	return status
}

func listenerSetPendingListenerStatuses(listenerSet gatewayv1.ListenerSet) []gatewayv1.ListenerEntryStatus {
	statuses := make([]gatewayv1.ListenerEntryStatus, 0, len(listenerSet.Spec.Listeners))
	for _, listener := range listenerSet.Spec.Listeners {
		statuses = append(statuses, gatewayv1.ListenerEntryStatus{
			Name:           listener.Name,
			SupportedKinds: supportedRouteKindsForListener(listenerFromListenerSetEntry(listener)),
		})
	}
	return statuses
}
