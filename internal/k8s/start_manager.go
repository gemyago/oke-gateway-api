package k8s

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-logr/logr"
	"go.uber.org/dig"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/gemyago/oke-gateway-api/internal/app"
	configtypes "github.com/gemyago/oke-gateway-api/internal/types"
)

// StartManagerDeps contains the dependencies for the controller manager.
type StartManagerDeps struct {
	dig.In

	RootLogger       *slog.Logger
	Manager          manager.Manager
	GatewayClassCtrl *app.GatewayClassController
	GatewayCtrl      *app.GatewayController
	NLBGatewayCtrl   *app.NetworkLoadBalancerGatewayController
	HTTPRouteCtrl    *app.HTTPRouteController
	GRPCRouteCtrl    *app.GRPCRouteController
	TCPRouteCtrl     *app.TCPRouteController
	UDPRouteCtrl     *app.UDPRouteController
	TLSRouteCtrl     *app.TLSRouteController
	BackendTLSCtrl   *app.BackendTLSPolicyController
	WatchesModel     *app.WatchesModel
	Config           *rest.Config

	// feature flags
	ReconcileGatewayClass               bool `name:"config.features.reconcileGatewayClass"`
	ReconcileGateway                    bool `name:"config.features.reconcileGateway"`
	ReconcileNetworkLoadBalancerGateway bool `name:"config.features.reconcileNetworkLoadBalancerGateway"`
	ReconcileTCPRoute                   bool `name:"config.features.reconcileTCPRoute"`
	ReconcileUDPRoute                   bool `name:"config.features.reconcileUDPRoute"`
	ReconcileTLSRoute                   bool `name:"config.features.reconcileTLSRoute"`
	ReconcileHTTPRoute                  bool `name:"config.features.reconcileHTTPRoute"`
	ReconcileGRPCRoute                  bool `name:"config.features.reconcileGRPCRoute"`
	ReconcileBackendTLSPolicy           bool `name:"config.features.reconcileBackendTLSPolicy"`
}

type experimentalRouteCapabilities struct {
	TCPRoute         bool
	UDPRoute         bool
	BackendTLSPolicy bool
}

type resolvedExperimentalRouteCapabilities struct {
	reconcileTCPRoute         bool
	reconcileUDPRoute         bool
	reconcileBackendTLSPolicy bool
	backendTLSPolicyAvailable bool
}

type setupL4RouteControllerParams struct {
	name                string
	route               client.Object
	mapEndpoint         handler.MapFunc
	mapGrant            handler.MapFunc
	mapGateway          handler.MapFunc
	mapSecret           handler.MapFunc
	mapBackendTLSPolicy handler.MapFunc
	mapConfigMap        handler.MapFunc
	mapService          handler.MapFunc
	reconciler          reconcile.TypedReconciler[reconcile.Request]
}

type setupL7RouteControllerParams struct {
	name                       string
	route                      client.Object
	mapEndpoint                handler.MapFunc
	pairedRoute                client.Object
	mapPairedRoute             handler.MapFunc
	mapBackendTLSPolicyToRoute handler.MapFunc
	mapConfigMapToRoute        handler.MapFunc
	mapServiceToRoute          handler.MapFunc
	reconciler                 reconcile.TypedReconciler[reconcile.Request]
}

type controllerSetupTask struct {
	enabled     bool
	disabledLog string
	setupErr    string
	setup       func() error
}

func l4RouteObjectPredicate() predicate.Funcs {
	generationChanged := predicate.GenerationChangedPredicate{}
	labelChanged := predicate.LabelChangedPredicate{}
	annotationChanged := predicate.AnnotationChangedPredicate{}
	return predicate.Funcs{
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return generationChanged.Update(updateEvent) ||
				labelChanged.Update(updateEvent) ||
				annotationChanged.Update(updateEvent)
		},
	}
}

func detectExperimentalRouteCapabilities(mapper meta.RESTMapper) (experimentalRouteCapabilities, error) {
	tcpRouteAvailable, err := resourceKindAvailable(
		mapper,
		schema.GroupKind{Group: gatewayv1.GroupName, Kind: "TCPRoute"},
		"v1alpha2",
	)
	if err != nil {
		return experimentalRouteCapabilities{}, fmt.Errorf("failed to detect TCPRoute availability: %w", err)
	}

	udpRouteAvailable, err := resourceKindAvailable(
		mapper,
		schema.GroupKind{Group: gatewayv1.GroupName, Kind: "UDPRoute"},
		"v1alpha2",
	)
	if err != nil {
		return experimentalRouteCapabilities{}, fmt.Errorf("failed to detect UDPRoute availability: %w", err)
	}
	backendTLSPolicyAvailable, err := resourceKindAvailable(
		mapper,
		schema.GroupKind{Group: gatewayv1.GroupName, Kind: "BackendTLSPolicy"},
		"v1",
	)
	if err != nil {
		return experimentalRouteCapabilities{}, fmt.Errorf("failed to detect BackendTLSPolicy availability: %w", err)
	}

	return experimentalRouteCapabilities{
		TCPRoute:         tcpRouteAvailable,
		UDPRoute:         udpRouteAvailable,
		BackendTLSPolicy: backendTLSPolicyAvailable,
	}, nil
}

func resourceKindAvailable(mapper meta.RESTMapper, groupKind schema.GroupKind, version string) (bool, error) {
	if _, err := mapper.RESTMapping(groupKind, version); err != nil {
		if meta.IsNoMatchError(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func runControllerSetupTasks(ctx context.Context, logger *slog.Logger, tasks []controllerSetupTask) error {
	for _, task := range tasks {
		if !task.enabled {
			logger.InfoContext(ctx, task.disabledLog)
			continue
		}
		if err := task.setup(); err != nil {
			return fmt.Errorf(task.setupErr, err)
		}
	}
	return nil
}

func resolveExperimentalRouteCapabilities(
	ctx context.Context,
	logger *slog.Logger,
	mapper meta.RESTMapper,
	deps StartManagerDeps,
) (resolvedExperimentalRouteCapabilities, error) {
	experimentalRouteCRDs, err := detectExperimentalRouteCapabilities(mapper)
	if err != nil {
		return resolvedExperimentalRouteCapabilities{}, err
	}
	if deps.ReconcileTCPRoute && !experimentalRouteCRDs.TCPRoute {
		logger.InfoContext(ctx, "TCPRoute CRD is not installed; TCPRoute support is disabled")
	}
	if deps.ReconcileUDPRoute && !experimentalRouteCRDs.UDPRoute {
		logger.InfoContext(ctx, "UDPRoute CRD is not installed; UDPRoute support is disabled")
	}
	if deps.ReconcileBackendTLSPolicy && !experimentalRouteCRDs.BackendTLSPolicy {
		logger.InfoContext(ctx, "BackendTLSPolicy CRD is not installed; BackendTLSPolicy support is disabled")
	}

	return resolvedExperimentalRouteCapabilities{
		reconcileTCPRoute:         deps.ReconcileTCPRoute && experimentalRouteCRDs.TCPRoute,
		reconcileUDPRoute:         deps.ReconcileUDPRoute && experimentalRouteCRDs.UDPRoute,
		reconcileBackendTLSPolicy: deps.ReconcileBackendTLSPolicy && experimentalRouteCRDs.BackendTLSPolicy,
		backendTLSPolicyAvailable: experimentalRouteCRDs.BackendTLSPolicy,
	}, nil
}

func setupL4RouteController(
	mgr manager.Manager,
	params setupL4RouteControllerParams,
	enableBackendTLSPolicy bool,
	middlewares ...controllerMiddleware[reconcile.Request],
) error {
	controllerBuilder := builder.ControllerManagedBy(mgr).
		Named(params.name).
		For(
			params.route,
			builder.WithPredicates(l4RouteObjectPredicate()),
		).
		Watches(
			&discoveryv1.EndpointSlice{},
			handler.EnqueueRequestsFromMapFunc(params.mapEndpoint),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&gatewayv1beta1.ReferenceGrant{},
			handler.EnqueueRequestsFromMapFunc(params.mapGrant),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Watches(
			&gatewayv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(params.mapGateway),
			builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})),
		)
	if params.mapSecret != nil {
		controllerBuilder = controllerBuilder.Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(params.mapSecret),
			builder.WithPredicates(gatewaySecretPredicate()),
		)
	}
	if enableBackendTLSPolicy && params.name == "tlsroute" {
		controllerBuilder = controllerBuilder.
			Watches(
				&gatewayv1.BackendTLSPolicy{},
				handler.EnqueueRequestsFromMapFunc(params.mapBackendTLSPolicy),
				builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
			).
			Watches(
				&corev1.ConfigMap{},
				handler.EnqueueRequestsFromMapFunc(params.mapConfigMap),
				builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
			).
			Watches(
				&corev1.Service{},
				handler.EnqueueRequestsFromMapFunc(params.mapService),
				builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
			)
	}
	return controllerBuilder.Complete(wireupReconciler(params.reconciler, middlewares...))
}

func coreControllerSetupTasks(
	mgr manager.Manager,
	deps StartManagerDeps,
	middlewares []controllerMiddleware[reconcile.Request],
) []controllerSetupTask {
	return []controllerSetupTask{
		{
			enabled:     deps.ReconcileGatewayClass,
			disabledLog: "GatewayClass controller is disabled",
			setupErr:    "failed to setup GatewayClass controller: %w",
			setup: func() error {
				return builder.ControllerManagedBy(mgr).
					Named("gatewayclass").
					For(&gatewayv1.GatewayClass{}).
					WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
					Complete(wireupReconciler(deps.GatewayClassCtrl, middlewares...))
			},
		},
		{
			enabled:     deps.ReconcileGateway,
			disabledLog: "Gateway controller is disabled",
			setupErr:    "failed to setup Gateway controller: %w",
			setup: func() error {
				return builder.ControllerManagedBy(mgr).
					Named("gateway").
					For(
						&gatewayv1.Gateway{},

						// Applying predicates just on the gateway level. Secrets do not have generation incremented
						// so secret updates will not trigger a reconciliation.
						builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})),
					).
					Watches(
						&corev1.Secret{},
						handler.EnqueueRequestsFromMapFunc(deps.WatchesModel.MapSecretToGateway),
						builder.WithPredicates(gatewaySecretPredicate()),
					).
					Watches(
						&configtypes.GatewayConfig{},
						handler.EnqueueRequestsFromMapFunc(deps.WatchesModel.MapGatewayConfigToGateway),
						builder.WithPredicates(predicate.GenerationChangedPredicate{}),
					).
					Complete(wireupReconciler(deps.GatewayCtrl, middlewares...))
			},
		},
		{
			enabled:     deps.ReconcileNetworkLoadBalancerGateway,
			disabledLog: "Network Load Balancer Gateway controller is disabled",
			setupErr:    "failed to setup Network Load Balancer Gateway controller: %w",
			setup: func() error {
				return builder.ControllerManagedBy(mgr).
					Named("networkloadbalancer-gateway").
					For(
						&gatewayv1.Gateway{},
						builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})),
					).
					Watches(
						&configtypes.GatewayConfig{},
						handler.EnqueueRequestsFromMapFunc(deps.WatchesModel.MapGatewayConfigToGateway),
						builder.WithPredicates(predicate.GenerationChangedPredicate{}),
					).
					Complete(wireupReconciler(deps.NLBGatewayCtrl, middlewares...))
			},
		},
	}
}

func l7AndTLSControllerSetupTasks(
	mgr manager.Manager,
	deps StartManagerDeps,
	experimentalRoutes resolvedExperimentalRouteCapabilities,
	middlewares []controllerMiddleware[reconcile.Request],
) []controllerSetupTask {
	return []controllerSetupTask{
		{
			enabled:     deps.ReconcileHTTPRoute,
			disabledLog: "HTTPRoute controller is disabled",
			setupErr:    "failed to setup HTTPRoute controller: %w",
			setup: func() error {
				return setupHTTPRouteController(mgr, deps, experimentalRoutes.reconcileBackendTLSPolicy, middlewares)
			},
		},
		{
			enabled:     deps.ReconcileGRPCRoute,
			disabledLog: "GRPCRoute controller is disabled",
			setupErr:    "failed to setup GRPCRoute controller: %w",
			setup: func() error {
				return setupGRPCRouteController(mgr, deps, experimentalRoutes.reconcileBackendTLSPolicy, middlewares)
			},
		},
		{
			enabled:     deps.ReconcileTLSRoute,
			disabledLog: "TLSRoute controller is disabled",
			setupErr:    "failed to setup TLSRoute controller: %w",
			setup: func() error {
				return setupL4RouteController(mgr, setupL4RouteControllerParams{
					name:                "tlsroute",
					route:               &gatewayv1.TLSRoute{},
					mapEndpoint:         deps.WatchesModel.MapEndpointSliceToTLSRoute,
					mapGrant:            deps.WatchesModel.MapReferenceGrantToTLSRoute,
					mapGateway:          deps.WatchesModel.MapGatewayToTLSRoute,
					mapSecret:           deps.WatchesModel.MapSecretToTLSRoute,
					mapBackendTLSPolicy: deps.WatchesModel.MapBackendTLSPolicyToTLSRoute,
					mapConfigMap:        deps.WatchesModel.MapConfigMapToTLSRoute,
					mapService:          deps.WatchesModel.MapServiceToTLSRoute,
					reconciler:          deps.TLSRouteCtrl,
				}, experimentalRoutes.reconcileBackendTLSPolicy, middlewares...)
			},
		},
		{
			enabled:     experimentalRoutes.backendTLSPolicyAvailable,
			disabledLog: "BackendTLSPolicy controller is disabled because the CRD is not installed",
			setupErr:    "failed to setup BackendTLSPolicy controller: %w",
			setup: func() error {
				return builder.ControllerManagedBy(mgr).
					Named("backendtlspolicy").
					For(&gatewayv1.BackendTLSPolicy{}).
					Complete(wireupReconciler(deps.BackendTLSCtrl, middlewares...))
			},
		},
	}
}

func setupHTTPRouteController(
	mgr manager.Manager,
	deps StartManagerDeps,
	enableBackendTLSPolicy bool,
	middlewares []controllerMiddleware[reconcile.Request],
) error {
	return setupL7RouteController(mgr, setupL7RouteControllerParams{
		name:                       "httproute",
		route:                      &gatewayv1.HTTPRoute{},
		mapEndpoint:                deps.WatchesModel.MapEndpointSliceToHTTPRoute,
		pairedRoute:                &gatewayv1.GRPCRoute{},
		mapPairedRoute:             deps.WatchesModel.MapGRPCRouteToHTTPRoute,
		mapBackendTLSPolicyToRoute: deps.WatchesModel.MapBackendTLSPolicyToHTTPRoute,
		mapConfigMapToRoute:        deps.WatchesModel.MapConfigMapToHTTPRoute,
		mapServiceToRoute:          deps.WatchesModel.MapServiceToHTTPRoute,
		reconciler:                 deps.HTTPRouteCtrl,
	}, enableBackendTLSPolicy, middlewares)
}

func setupGRPCRouteController(
	mgr manager.Manager,
	deps StartManagerDeps,
	enableBackendTLSPolicy bool,
	middlewares []controllerMiddleware[reconcile.Request],
) error {
	return setupL7RouteController(mgr, setupL7RouteControllerParams{
		name:                       "grpcroute",
		route:                      &gatewayv1.GRPCRoute{},
		mapEndpoint:                deps.WatchesModel.MapEndpointSliceToGRPCRoute,
		pairedRoute:                &gatewayv1.HTTPRoute{},
		mapPairedRoute:             deps.WatchesModel.MapHTTPRouteToGRPCRoute,
		mapBackendTLSPolicyToRoute: deps.WatchesModel.MapBackendTLSPolicyToGRPCRoute,
		mapConfigMapToRoute:        deps.WatchesModel.MapConfigMapToGRPCRoute,
		mapServiceToRoute:          deps.WatchesModel.MapServiceToGRPCRoute,
		reconciler:                 deps.GRPCRouteCtrl,
	}, enableBackendTLSPolicy, middlewares)
}

func setupL7RouteController(
	mgr manager.Manager,
	params setupL7RouteControllerParams,
	enableBackendTLSPolicy bool,
	middlewares []controllerMiddleware[reconcile.Request],
) error {
	controllerBuilder := builder.ControllerManagedBy(mgr).
		Named(params.name).
		For(params.route).
		Watches(
			&discoveryv1.EndpointSlice{},
			handler.EnqueueRequestsFromMapFunc(params.mapEndpoint),
		).
		Watches(
			params.pairedRoute,
			handler.EnqueueRequestsFromMapFunc(params.mapPairedRoute),
			builder.WithPredicates(l7RouteObjectPredicate()),
		).
		WithEventFilter(l7RouteObjectPredicate())
	if enableBackendTLSPolicy {
		controllerBuilder = controllerBuilder.
			Watches(
				&gatewayv1.BackendTLSPolicy{},
				handler.EnqueueRequestsFromMapFunc(params.mapBackendTLSPolicyToRoute),
				builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
			).
			Watches(
				&corev1.ConfigMap{},
				handler.EnqueueRequestsFromMapFunc(params.mapConfigMapToRoute),
				builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
			).
			Watches(
				&corev1.Service{},
				handler.EnqueueRequestsFromMapFunc(params.mapServiceToRoute),
				builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
			)
	}
	return controllerBuilder.Complete(wireupReconciler(params.reconciler, middlewares...))
}

func l4RouteControllerSetupTasks(
	mgr manager.Manager,
	deps StartManagerDeps,
	experimentalRoutes resolvedExperimentalRouteCapabilities,
	middlewares []controllerMiddleware[reconcile.Request],
) []controllerSetupTask {
	return []controllerSetupTask{
		{
			enabled:     experimentalRoutes.reconcileTCPRoute,
			disabledLog: "TCPRoute controller is disabled",
			setupErr:    "failed to setup TCPRoute controller: %w",
			setup: func() error {
				return setupL4RouteController(mgr, setupL4RouteControllerParams{
					name:        "tcproute",
					route:       &gatewayv1alpha2.TCPRoute{},
					mapEndpoint: deps.WatchesModel.MapEndpointSliceToTCPRoute,
					mapGrant:    deps.WatchesModel.MapReferenceGrantToTCPRoute,
					mapGateway:  deps.WatchesModel.MapGatewayToTCPRoute,
					reconciler:  deps.TCPRouteCtrl,
				}, false, middlewares...)
			},
		},
		{
			enabled:     experimentalRoutes.reconcileUDPRoute,
			disabledLog: "UDPRoute controller is disabled",
			setupErr:    "failed to setup UDPRoute controller: %w",
			setup: func() error {
				return setupL4RouteController(mgr, setupL4RouteControllerParams{
					name:        "udproute",
					route:       &gatewayv1alpha2.UDPRoute{},
					mapEndpoint: deps.WatchesModel.MapEndpointSliceToUDPRoute,
					mapGrant:    deps.WatchesModel.MapReferenceGrantToUDPRoute,
					mapGateway:  deps.WatchesModel.MapGatewayToUDPRoute,
					reconciler:  deps.UDPRouteCtrl,
				}, false, middlewares...)
			},
		},
	}
}

func l7RouteObjectPredicate() predicate.Funcs {
	generationChanged := predicate.GenerationChangedPredicate{}
	labelChanged := predicate.LabelChangedPredicate{}
	annotationChanged := predicate.AnnotationChangedPredicate{}
	return predicate.Funcs{
		UpdateFunc: func(updateEvent event.UpdateEvent) bool {
			return generationChanged.Update(updateEvent) ||
				labelChanged.Update(updateEvent) ||
				annotationChanged.Update(updateEvent)
		},
	}
}

func gatewaySecretPredicate() predicate.Funcs {
	resourceVersionChanged := predicate.ResourceVersionChangedPredicate{}
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool { return true },
		UpdateFunc: resourceVersionChanged.Update,
	}
}

// StartManager starts the controller manager.
func StartManager(ctx context.Context, deps StartManagerDeps) error {
	logger := deps.RootLogger.WithGroup("k8s")

	rlogLogger := logr.FromSlogHandler(logger.Handler())
	loggerCtx := logr.NewContext(ctx, rlogLogger)
	log.SetLogger(rlogLogger)

	mgr := deps.Manager

	experimentalRoutes, detectErr := resolveExperimentalRouteCapabilities(loggerCtx, logger, mgr.GetRESTMapper(), deps)
	if detectErr != nil {
		return detectErr
	}

	if err := deps.WatchesModel.RegisterFieldIndexers(ctx, mgr.GetFieldIndexer(), app.RegisterFieldIndexersOptions{
		EnableTCPRoute: experimentalRoutes.reconcileTCPRoute,
		EnableUDPRoute: experimentalRoutes.reconcileUDPRoute,
		EnableTLSRoute: deps.ReconcileTLSRoute,
	}); err != nil {
		return fmt.Errorf("failed to register field indexers: %w", err)
	}

	middlewares := []controllerMiddleware[reconcile.Request]{
		newTracingMiddleware(),
		newErrorHandlingMiddleware(deps.RootLogger),
	}
	tasks := coreControllerSetupTasks(mgr, deps, middlewares)
	tasks = append(tasks, l7AndTLSControllerSetupTasks(mgr, deps, experimentalRoutes, middlewares)...)
	tasks = append(tasks, l4RouteControllerSetupTasks(mgr, deps, experimentalRoutes, middlewares)...)
	if err := runControllerSetupTasks(loggerCtx, logger, tasks); err != nil {
		return err
	}

	logger.InfoContext(loggerCtx, "Starting controller manager")
	return mgr.Start(loggerCtx)
}
