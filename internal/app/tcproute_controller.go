package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TCPRouteController reconciles TCPRoute resources for OCI Network Load Balancer.
type TCPRouteController struct {
	logger        *slog.Logger
	tcpRouteModel tcpRouteModel
}

type TCPRouteControllerDeps struct {
	dig.In

	RootLogger    *slog.Logger
	TCPRouteModel tcpRouteModel
}

func NewTCPRouteController(deps TCPRouteControllerDeps) *TCPRouteController {
	return &TCPRouteController{
		logger:        deps.RootLogger.WithGroup("tcproute-controller"),
		tcpRouteModel: deps.TCPRouteModel,
	}
}

func (r *TCPRouteController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	return reconcileL4Route(ctx, reconcileL4RouteParams[resolvedTCPRouteDetails]{
		logger:        r.logger,
		req:           req,
		routeKind:     "TCPRoute",
		routeAttr:     "tcpRoute",
		finalizer:     NetworkLoadBalancerTCPRouteProgrammedFinalizer,
		resolve:       r.tcpRouteModel.resolveRequest,
		route:         func(details resolvedTCPRouteDetails) client.Object { return &details.tcpRoute },
		deprovision:   r.tcpRouteModel.deprovisionRoute,
		program:       r.tcpRouteModel.programRoute,
		setProgrammed: r.tcpRouteModel.setProgrammed,
		setRejected: func(details resolvedTCPRouteDetails, err error) (bool, error) {
			var statusErr tcpRouteStatusError
			if errors.As(err, &statusErr) {
				return true, r.tcpRouteModel.setRejected(ctx, details, statusErr)
			}
			return false, nil
		},
	})
}

type reconcileL4RouteParams[D any] struct {
	logger        *slog.Logger
	req           reconcile.Request
	routeKind     string
	routeAttr     string
	finalizer     string
	resolve       func(context.Context, reconcile.Request) ([]D, error)
	route         func(D) client.Object
	deprovision   func(context.Context, D) error
	program       func(context.Context, D) error
	setProgrammed func(context.Context, D) error
	setRejected   func(D, error) (bool, error)
}

func reconcileL4Route[D any](ctx context.Context, params reconcileL4RouteParams[D]) (reconcile.Result, error) {
	params.logger.InfoContext(
		ctx,
		fmt.Sprintf("Processing reconciliation for %s %s", params.routeKind, params.req.NamespacedName),
	)

	resolvedRoutes, err := params.resolve(ctx, params.req)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to resolve %s parent: %w", params.routeKind, err)
	}
	if len(resolvedRoutes) == 0 {
		params.logger.DebugContext(ctx, fmt.Sprintf("Ignoring irrelevant %s", params.routeKind),
			slog.String(params.routeAttr, params.req.NamespacedName.String()),
		)
		return reconcile.Result{}, nil
	}

	for _, resolvedRoute := range resolvedRoutes {
		if err = reconcileResolvedL4Route(ctx, params, resolvedRoute); err != nil {
			var busyErr *networkLoadBalancerBusyError
			if errors.As(err, &busyErr) {
				params.logger.InfoContext(ctx, "OCI Network Load Balancer is busy, requeueing route",
					slog.String(params.routeAttr, params.req.NamespacedName.String()),
					slog.String("error", busyErr.Error()),
				)
				return networkLoadBalancerBusyRequeue(), nil
			}
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

func reconcileResolvedL4Route[D any](
	ctx context.Context,
	params reconcileL4RouteParams[D],
	resolvedRoute D,
) error {
	route := params.route(resolvedRoute)
	if route.GetDeletionTimestamp() != nil {
		if !controllerutil.ContainsFinalizer(route, params.finalizer) {
			return nil
		}
		if err := params.deprovision(ctx, resolvedRoute); err != nil {
			return fmt.Errorf("failed to deprovision %s %s: %w", params.routeKind, params.req.NamespacedName, err)
		}
		return nil
	}

	if err := params.program(ctx, resolvedRoute); err != nil {
		return handleL4RouteProgramError(params, resolvedRoute, err)
	}
	if err := params.setProgrammed(ctx, resolvedRoute); err != nil {
		return fmt.Errorf("failed to set %s %s programmed status: %w", params.routeKind, params.req.NamespacedName, err)
	}
	return nil
}

func handleL4RouteProgramError[D any](
	params reconcileL4RouteParams[D],
	resolvedRoute D,
	err error,
) error {
	handled, statusErr := params.setRejected(resolvedRoute, err)
	if statusErr != nil {
		return fmt.Errorf(
			"failed to set %s %s rejected status: %w",
			params.routeKind,
			params.req.NamespacedName,
			statusErr,
		)
	}
	if handled {
		return nil
	}
	return fmt.Errorf("failed to program %s %s: %w", params.routeKind, params.req.NamespacedName, err)
}
