package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/samber/lo"
	"go.uber.org/dig"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// NetworkLoadBalancerGatewayController reconciles Gateway resources for OCI Network Load Balancer.
type NetworkLoadBalancerGatewayController struct {
	logger         *slog.Logger
	resourcesModel resourcesModel
	gatewayModel   networkLoadBalancerGatewayModel
	driftInterval  time.Duration
}

type NetworkLoadBalancerGatewayControllerDeps struct {
	dig.In

	RootLogger     *slog.Logger
	ResourcesModel resourcesModel
	GatewayModel   networkLoadBalancerGatewayModel
	DriftInterval  time.Duration `name:"config.reconcile.drift-interval"`
}

func NewNetworkLoadBalancerGatewayController(
	deps NetworkLoadBalancerGatewayControllerDeps,
) *NetworkLoadBalancerGatewayController {
	return &NetworkLoadBalancerGatewayController{
		logger:         deps.RootLogger.WithGroup("network-load-balancer-gateway-controller"),
		resourcesModel: deps.ResourcesModel,
		gatewayModel:   deps.GatewayModel,
		driftInterval:  deps.DriftInterval,
	}
}

func (r *NetworkLoadBalancerGatewayController) SetListenerSetEnabled(enabled bool) {
	if model, ok := r.gatewayModel.(interface{ setListenerSetEnabled(bool) }); ok {
		model.setListenerSetEnabled(enabled)
	}
}

func (r *NetworkLoadBalancerGatewayController) processResourceError(
	ctx context.Context,
	err error,
	gateway *gatewayv1.Gateway,
) (reconcile.Result, error) {
	var busyErr *networkLoadBalancerBusyError
	if errors.As(err, &busyErr) {
		r.logger.InfoContext(ctx, "OCI Network Load Balancer is busy, requeueing Gateway",
			slog.String("gateway", gateway.Name),
			slog.String("error", busyErr.Error()),
		)
		return networkLoadBalancerBusyRequeue(), nil
	}
	var reasonErr *resourceStatusError
	if errors.As(err, &reasonErr) {
		if err = r.resourcesModel.setCondition(ctx, setConditionParams{
			resource:      gateway,
			conditions:    &gateway.Status.Conditions,
			conditionType: reasonErr.conditionType,
			status:        metav1.ConditionFalse,
			reason:        reasonErr.reason,
			message:       reasonErr.message,
		}); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to set condition for Gateway %s: %w", gateway.Name, err)
		}
		return driftRequeue(r.driftInterval), nil
	}
	return reconcile.Result{}, fmt.Errorf("failed to program Network Load Balancer Gateway %s: %w", gateway.Name, err)
}

func (r *NetworkLoadBalancerGatewayController) Reconcile(
	ctx context.Context,
	req reconcile.Request,
) (reconcile.Result, error) {
	var data resolvedGatewayDetails
	relevant, err := r.gatewayModel.resolveReconcileRequest(ctx, req, &data)
	if err != nil {
		return r.processResourceError(ctx, err, &data.gateway)
	}
	if !relevant {
		return reconcile.Result{}, nil
	}

	if data.gateway.DeletionTimestamp != nil {
		if !lo.Contains(data.gateway.Finalizers, NetworkLoadBalancerGatewayProgrammedFinalizer) {
			return reconcile.Result{}, nil
		}
		if err = r.gatewayModel.deprovisionGateway(ctx, &data); err != nil {
			return r.processResourceError(ctx, err, &data.gateway)
		}
		return reconcile.Result{}, nil
	}

	if !r.resourcesModel.isConditionSet(isConditionSetParams{
		resource:      &data.gateway,
		conditions:    data.gateway.Status.Conditions,
		conditionType: string(gatewayv1.GatewayConditionAccepted),
	}) {
		if err = r.resourcesModel.setCondition(ctx, setConditionParams{
			resource:      &data.gateway,
			conditions:    &data.gateway.Status.Conditions,
			conditionType: string(gatewayv1.GatewayConditionAccepted),
			status:        metav1.ConditionTrue,
			reason:        string(gatewayv1.GatewayReasonAccepted),
			message: fmt.Sprintf(
				"Gateway %s accepted by %s",
				data.gateway.Name,
				NetworkLoadBalancerControllerClassName,
			),
			annotations: map[string]string{
				NetworkLoadBalancerControllerClassName: "true",
			},
		}); err != nil {
			return reconcile.Result{}, fmt.Errorf(
				"failed to set accepted condition for Gateway %s: %w",
				req.NamespacedName,
				err,
			)
		}
	}

	if r.gatewayModel.isProgrammed(ctx, &data) && r.driftInterval <= 0 {
		r.logger.DebugContext(ctx, "Network Load Balancer Gateway already programmed",
			slog.String("gateway", req.NamespacedName.String()),
		)
		return reconcile.Result{}, nil
	}

	if err = r.gatewayModel.programGateway(ctx, &data); err != nil {
		return r.processResourceError(ctx, err, &data.gateway)
	}
	nlb, err := r.gatewayModel.getNetworkLoadBalancer(ctx, &data)
	if err != nil {
		var statusErr *resourceStatusError
		if errors.As(err, &statusErr) {
			return r.processResourceError(ctx, err, &data.gateway)
		}
		return reconcile.Result{}, fmt.Errorf("failed to get programmed Network Load Balancer: %w", err)
	}
	if err = r.gatewayModel.setProgrammed(ctx, &data, nlb); err != nil {
		return reconcile.Result{}, err
	}
	return driftRequeue(r.driftInterval), nil
}
