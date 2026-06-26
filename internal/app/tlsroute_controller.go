package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TLSRouteController reconciles TLSRoute resources for OCI Load Balancer and Network Load Balancer.
type TLSRouteController struct {
	logger        *slog.Logger
	tlsRouteModel tlsRouteModel
	driftInterval time.Duration
}

type TLSRouteControllerDeps struct {
	dig.In

	RootLogger    *slog.Logger
	TLSRouteModel tlsRouteModel
	DriftInterval time.Duration `name:"config.reconcile.drift-interval"`
}

func NewTLSRouteController(deps TLSRouteControllerDeps) *TLSRouteController {
	return &TLSRouteController{
		logger:        deps.RootLogger.WithGroup("tlsroute-controller"),
		tlsRouteModel: deps.TLSRouteModel,
		driftInterval: deps.DriftInterval,
	}
}

func (r *TLSRouteController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.logger.InfoContext(ctx, fmt.Sprintf("Processing reconciliation for TLSRoute %s", req.NamespacedName))

	resolvedRoutes, err := r.tlsRouteModel.resolveRequest(ctx, req)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to resolve TLSRoute parent: %w", err)
	}
	if len(resolvedRoutes) == 0 {
		r.logger.DebugContext(ctx, "Ignoring irrelevant TLSRoute",
			slog.String("tlsRoute", req.NamespacedName.String()),
		)
		return reconcile.Result{}, nil
	}

	for _, resolvedRoute := range resolvedRoutes {
		if err = r.reconcileResolvedRoute(ctx, req, resolvedRoute); err != nil {
			var busyErr *networkLoadBalancerBusyError
			if errors.As(err, &busyErr) {
				r.logger.InfoContext(ctx, "OCI Network Load Balancer is busy, requeueing TLSRoute",
					slog.String("tlsRoute", req.NamespacedName.String()),
					slog.String("error", busyErr.Error()),
				)
				return networkLoadBalancerBusyRequeue(), nil
			}
			return reconcile.Result{}, err
		}
	}

	return driftRequeue(r.driftInterval), nil
}

func (r *TLSRouteController) reconcileResolvedRoute(
	ctx context.Context,
	req reconcile.Request,
	resolvedRoute resolvedTLSRouteDetails,
) error {
	route := &resolvedRoute.tlsRoute
	if route.GetDeletionTimestamp() != nil {
		finalizer := tlsRouteFinalizerForDetails(resolvedRoute)
		if !controllerutil.ContainsFinalizer(route, finalizer) {
			return nil
		}
		if err := r.tlsRouteModel.deprovisionRoute(ctx, resolvedRoute); err != nil {
			return fmt.Errorf("failed to deprovision TLSRoute %s: %w", req.NamespacedName, err)
		}
		return nil
	}

	if err := r.tlsRouteModel.programRoute(ctx, resolvedRoute); err != nil {
		var statusErr tlsRouteStatusError
		if errors.As(err, &statusErr) {
			if rejectErr := r.tlsRouteModel.setRejected(ctx, resolvedRoute, statusErr); rejectErr != nil {
				return fmt.Errorf("failed to set TLSRoute %s rejected status: %w", req.NamespacedName, rejectErr)
			}
			return nil
		}
		return fmt.Errorf("failed to program TLSRoute %s: %w", req.NamespacedName, err)
	}
	if err := r.tlsRouteModel.setProgrammed(ctx, resolvedRoute); err != nil {
		return fmt.Errorf("failed to set TLSRoute %s programmed status: %w", req.NamespacedName, err)
	}
	return nil
}

func tlsRouteFinalizerForDetails(details resolvedTLSRouteDetails) string {
	if details.gatewayDetails.gatewayClass.Spec.ControllerName == ControllerClassName {
		return LoadBalancerTLSRouteProgrammedFinalizer
	}
	return NetworkLoadBalancerTLSRouteProgrammedFinalizer
}
