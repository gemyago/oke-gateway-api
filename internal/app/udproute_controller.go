package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.uber.org/dig"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// UDPRouteController reconciles UDPRoute resources for OCI Network Load Balancer.
type UDPRouteController struct {
	logger        *slog.Logger
	udpRouteModel udpRouteModel
	driftInterval time.Duration
}

type UDPRouteControllerDeps struct {
	dig.In

	RootLogger    *slog.Logger
	UDPRouteModel udpRouteModel
	DriftInterval time.Duration `name:"config.reconcile.drift-interval"`
}

func NewUDPRouteController(deps UDPRouteControllerDeps) *UDPRouteController {
	return &UDPRouteController{
		logger:        deps.RootLogger.WithGroup("udproute-controller"),
		udpRouteModel: deps.UDPRouteModel,
		driftInterval: deps.DriftInterval,
	}
}

func (r *UDPRouteController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	return reconcileL4Route(ctx, reconcileL4RouteParams[resolvedUDPRouteDetails]{
		logger:        r.logger,
		req:           req,
		routeKind:     "UDPRoute",
		routeAttr:     "udpRoute",
		finalizer:     NetworkLoadBalancerUDPRouteProgrammedFinalizer,
		resolve:       r.udpRouteModel.resolveRequest,
		route:         func(details resolvedUDPRouteDetails) client.Object { return &details.udpRoute },
		deprovision:   r.udpRouteModel.deprovisionRoute,
		program:       r.udpRouteModel.programRoute,
		setProgrammed: r.udpRouteModel.setProgrammed,
		driftInterval: r.driftInterval,
		setRejected: func(details resolvedUDPRouteDetails, err error) (bool, error) {
			var statusErr udpRouteStatusError
			if errors.As(err, &statusErr) {
				return true, r.udpRouteModel.setRejected(ctx, details, statusErr)
			}
			return false, nil
		},
	})
}
