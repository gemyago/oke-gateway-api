package app

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/dig"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const ControllerClassName = "oke-gateway-api.gemyago.github.io/oke-alb-gateway-controller"

// This is an internal interface used only to describe what we need from the client.
type k8sClient interface {
	Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
	List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
	Status() client.StatusWriter
}

// GatewayClassController is a simple controller that watches GatewayClass resources.
type GatewayClassController struct {
	client k8sClient
	logger *slog.Logger
}

// GatewayClassControllerDeps contains the dependencies for the GatewayClassController.
type GatewayClassControllerDeps struct {
	dig.In

	RootLogger *slog.Logger
	K8sClient  k8sClient
}

// NewGatewayClassController creates a new GatewayClassController.
func NewGatewayClassController(deps GatewayClassControllerDeps) *GatewayClassController {
	return &GatewayClassController{
		client: deps.K8sClient,
		logger: deps.RootLogger,
	}
}

// Reconcile implements the reconcile.Reconciler interface.
func (r *GatewayClassController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.logger.InfoContext(ctx, fmt.Sprintf("Reconciling GatewayClass %s", req.NamespacedName))
	var gatewayClass gatewayv1.GatewayClass
	if err := r.client.Get(ctx, req.NamespacedName, &gatewayClass); err != nil {
		if errors.IsNotFound(err) {
			r.logger.InfoContext(ctx, fmt.Sprintf("GatewayClass %s removed", req.NamespacedName))
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get GatewayClass %s: %w", req.NamespacedName, err)
	}

	// Check if the ControllerName matches the one we are responsible for
	if gatewayClass.Spec.ControllerName != ControllerClassName {
		r.logger.DebugContext(ctx,
			"Ignoring GatewayClass because controllerName does not match",
			slog.String("gatewayClass", req.NamespacedName.String()),
			slog.String("expectedControllerName", ControllerClassName),
			slog.String("actualControllerName", string(gatewayClass.Spec.ControllerName)),
		)
		return reconcile.Result{}, nil // Ignore this GatewayClass
	}

	r.logger.DebugContext(ctx, "Performing reconciliation",
		slog.Any("req", req),
		slog.Any("gatewayClass", gatewayClass),
	)

	// For now, we just log the reconciliation and do nothing
	// Update the status condition
	acceptedCondition := metav1.Condition{
		Type:               "Accepted",
		Status:             metav1.ConditionTrue,
		Reason:             "Accepted",
		Message:            "GatewayClass is accepted by this controller",
		ObservedGeneration: gatewayClass.Generation,
		LastTransitionTime: metav1.Now(),
	}

	meta.SetStatusCondition(&gatewayClass.Status.Conditions, acceptedCondition)

	// Update the status subresource
	if err := r.client.Status().Update(ctx, &gatewayClass); err != nil {
		r.logger.ErrorContext(ctx, "Failed to update GatewayClass status", slog.Any("error", err))
		return reconcile.Result{}, fmt.Errorf("failed to update GatewayClass status for %s: %w", req.NamespacedName, err)
	}

	r.logger.InfoContext(ctx,
		"Successfully reconciled and accepted GatewayClass",
		slog.Any("gatewayClass", req.NamespacedName),
	)
	return reconcile.Result{}, nil
}
