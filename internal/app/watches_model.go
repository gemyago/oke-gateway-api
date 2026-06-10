package app

import (
	"context"
	"fmt"
	"log/slog"
	"path"

	"github.com/samber/lo"
	"go.uber.org/dig"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	configtypes "github.com/gemyago/oke-gateway-api/internal/types"
)

const httpRouteBackendServiceIndexKey = ".metadata.backendRefs.serviceName" // Virtual field name, indexed
const grpcRouteBackendServiceIndexKey = ".metadata.grpcBackendRefs.serviceName"
const gatewayCertificateIndexKey = ".metadata.certificates" // Virtual field name, indexed
const tcpRouteBackendServiceIndexKey = ".metadata.tcpBackendRefs.serviceName"
const udpRouteBackendServiceIndexKey = ".metadata.udpBackendRefs.serviceName"

// WatchesModel implements the WatchesModel interface.
type WatchesModel struct {
	k8sClient k8sClient
	logger    *slog.Logger
}

type WatchesModelDeps struct {
	dig.In

	K8sClient k8sClient
	Logger    *slog.Logger
}

// RegisterFieldIndexersOptions controls which optional route indexers are registered.
type RegisterFieldIndexersOptions struct {
	EnableTCPRoute bool
	EnableUDPRoute bool
}

// NewWatchesModel creates a new watchesModel.
func NewWatchesModel(deps WatchesModelDeps) *WatchesModel {
	return &WatchesModel{
		k8sClient: deps.K8sClient,
		logger:    deps.Logger.WithGroup("watches-model"),
	}
}

// RegisterFieldIndexers registers the indexers for the watches model.
func (m *WatchesModel) RegisterFieldIndexers(
	ctx context.Context,
	indexer client.FieldIndexer,
	options ...RegisterFieldIndexersOptions,
) error {
	opts := RegisterFieldIndexersOptions{
		EnableTCPRoute: true,
		EnableUDPRoute: true,
	}
	if len(options) > 0 {
		opts = options[0]
	}

	if err := indexer.IndexField(ctx,
		&gatewayv1.HTTPRoute{},
		httpRouteBackendServiceIndexKey,
		func(o client.Object) []string {
			return m.indexHTTPRouteByBackendService(ctx, o)
		},
	); err != nil {
		return fmt.Errorf("failed to index HTTPRoute by backend service: %w", err)
	}

	if err := indexer.IndexField(ctx,
		&gatewayv1.GRPCRoute{},
		grpcRouteBackendServiceIndexKey,
		func(o client.Object) []string {
			return m.indexGRPCRouteByBackendService(ctx, o)
		},
	); err != nil {
		return fmt.Errorf("failed to index GRPCRoute by backend service: %w", err)
	}

	if opts.EnableTCPRoute {
		if err := indexer.IndexField(ctx,
			&gatewayv1alpha2.TCPRoute{},
			tcpRouteBackendServiceIndexKey,
			func(o client.Object) []string {
				return m.indexTCPRouteByBackendService(ctx, o)
			},
		); err != nil {
			return fmt.Errorf("failed to index TCPRoute by backend service: %w", err)
		}
	}

	if opts.EnableUDPRoute {
		if err := indexer.IndexField(ctx,
			&gatewayv1alpha2.UDPRoute{},
			udpRouteBackendServiceIndexKey,
			func(o client.Object) []string {
				return m.indexUDPRouteByBackendService(ctx, o)
			},
		); err != nil {
			return fmt.Errorf("failed to index UDPRoute by backend service: %w", err)
		}
	}

	if err := indexer.IndexField(ctx,
		&gatewayv1.Gateway{},
		gatewayCertificateIndexKey,
		func(o client.Object) []string {
			return m.indexGatewayByCertificateSecrets(ctx, o)
		},
	); err != nil {
		return fmt.Errorf("failed to index Gateway by certificate: %w", err)
	}

	m.logger.DebugContext(ctx, "Field indexers registered",
		slog.String("indexKey", httpRouteBackendServiceIndexKey),
		slog.String("indexKey", grpcRouteBackendServiceIndexKey),
		slog.String("indexKey", gatewayCertificateIndexKey),
		slog.Bool("tcpRouteIndexEnabled", opts.EnableTCPRoute),
		slog.Bool("udpRouteIndexEnabled", opts.EnableUDPRoute),
	)
	return nil
}

func (m *WatchesModel) indexGRPCRouteByBackendService(ctx context.Context, obj client.Object) []string {
	grpcRoute, isRoute := obj.(*gatewayv1.GRPCRoute)
	logger := m.logger.WithGroup("grpc-route-backend-service-index")
	if !isRoute {
		logger.WarnContext(ctx, "Received non-GRPCRoute object", slog.Any("object", obj))
		return nil
	}
	if grpcRoute.DeletionTimestamp != nil {
		return nil
	}

	matchingParentStatus, found := lo.Find(
		grpcRoute.Status.Parents,
		func(status gatewayv1.RouteParentStatus) bool {
			return status.ControllerName == ControllerClassName
		})
	if !found {
		return nil
	}
	if condition := meta.FindStatusCondition(
		matchingParentStatus.Conditions,
		string(gatewayv1.RouteConditionResolvedRefs),
	); condition == nil || condition.Status != v1.ConditionTrue {
		return nil
	}

	uniqueServiceKeys := make(map[string]struct{})
	for _, rule := range grpcRoute.Spec.Rules {
		for _, key := range indexBackendRefsByService(grpcRoute.Namespace, grpcBackendRefsToBackendRefs(rule.BackendRefs)) {
			uniqueServiceKeys[key] = struct{}{}
		}
	}

	serviceKeys := lo.Keys(uniqueServiceKeys)
	logger.DebugContext(ctx, "Indexed GRPCRoute by backend service",
		slog.String("grpcRoute", client.ObjectKeyFromObject(grpcRoute).String()),
		slog.String("indexKey", grpcRouteBackendServiceIndexKey),
		slog.Any("serviceKeys", serviceKeys),
	)
	return serviceKeys
}

func grpcBackendRefsToBackendRefs(refs []gatewayv1.GRPCBackendRef) []gatewayv1.BackendRef {
	backendRefs := make([]gatewayv1.BackendRef, 0, len(refs))
	for _, ref := range refs {
		backendRefs = append(backendRefs, ref.BackendRef)
	}
	return backendRefs
}

func indexBackendRefsByService(namespace string, refs []gatewayv1.BackendRef) []string {
	uniqueServiceKeys := make(map[string]struct{})
	for _, backendRef := range refs {
		ns := namespace
		if backendRef.BackendObjectReference.Namespace != nil {
			ns = string(*backendRef.BackendObjectReference.Namespace)
		}
		namespacedName := path.Join(ns, string(backendRef.BackendObjectReference.Name))
		if _, ok := uniqueServiceKeys[namespacedName]; !ok {
			uniqueServiceKeys[namespacedName] = struct{}{}
		}
	}
	return lo.Keys(uniqueServiceKeys)
}

// indexHTTPRouteByBackendService extracts the namespaced names of Services referenced
// in an HTTPRoute's backendRefs. This is used to create an index for efficient
// lookup when an EndpointSlice changes.
func (m *WatchesModel) indexHTTPRouteByBackendService(ctx context.Context, obj client.Object) []string {
	httpRoute, isRoute := obj.(*gatewayv1.HTTPRoute)
	logger := m.logger.WithGroup("http-route-backend-service-index")
	if !isRoute {
		logger.WarnContext(ctx, "Received non-HTTPRoute object", slog.Any("object", obj))
		return nil
	}

	if httpRoute.DeletionTimestamp != nil {
		logger.DebugContext(ctx, "Ignoring HTTPRoute marked for deletion",
			slog.String("httpRoute", client.ObjectKeyFromObject(httpRoute).String()),
			slog.Time("deletionTimestamp", httpRoute.DeletionTimestamp.Time),
		)
		return nil
	}

	matchingParentStatus, found := lo.Find(
		httpRoute.Status.Parents,
		func(status gatewayv1.RouteParentStatus) bool {
			return status.ControllerName == ControllerClassName
		})
	if !found {
		logger.DebugContext(ctx, "HTTPRoute is not accepted by this controller. Skipping indexing",
			slog.String("httpRoute", client.ObjectKeyFromObject(httpRoute).String()),
		)
		return nil
	}

	if condition := meta.FindStatusCondition(
		matchingParentStatus.Conditions,

		// This status is set by the controller when it's programmed
		// we should probably create a custom status, bit it is like below for now
		string(gatewayv1.RouteConditionResolvedRefs),
	); condition == nil || condition.Status != v1.ConditionTrue {
		logger.DebugContext(ctx, "HTTPRoute is not programmed by this controller. Skipping indexing",
			slog.String("httpRoute", client.ObjectKeyFromObject(httpRoute).String()),
		)
		return nil
	}

	uniqueServiceKeys := make(map[string]struct{})
	for _, rule := range httpRoute.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			ns := httpRoute.Namespace
			if backendRef.BackendObjectReference.Namespace != nil {
				ns = string(*backendRef.BackendObjectReference.Namespace)
			}
			namespacedName := path.Join(
				ns,
				string(backendRef.BackendObjectReference.Name),
			)
			if _, ok := uniqueServiceKeys[namespacedName]; !ok {
				uniqueServiceKeys[namespacedName] = struct{}{}
			}
		}
	}

	serviceKeys := lo.Keys(uniqueServiceKeys)
	logger.DebugContext(ctx, "Indexed HTTPRoute by backend service",
		slog.String("httpRoute", client.ObjectKeyFromObject(httpRoute).String()),
		slog.String("indexKey", httpRouteBackendServiceIndexKey),
		slog.Any("serviceKeys", serviceKeys),
	)

	return serviceKeys
}

func (m *WatchesModel) indexTCPRouteByBackendService(ctx context.Context, obj client.Object) []string {
	tcpRoute, isRoute := obj.(*gatewayv1alpha2.TCPRoute)
	logger := m.logger.WithGroup("tcp-route-backend-service-index")
	if !isRoute {
		logger.WarnContext(ctx, "Received non-TCPRoute object", slog.Any("object", obj))
		return nil
	}
	if tcpRoute.DeletionTimestamp != nil {
		logger.DebugContext(ctx, "Ignoring TCPRoute marked for deletion",
			slog.String("tcpRoute", client.ObjectKeyFromObject(tcpRoute).String()),
			slog.Time("deletionTimestamp", tcpRoute.DeletionTimestamp.Time),
		)
		return nil
	}

	rulesBackendRefs := make([][]gatewayv1.BackendRef, 0, len(tcpRoute.Spec.Rules))
	for _, rule := range tcpRoute.Spec.Rules {
		rulesBackendRefs = append(rulesBackendRefs, rule.BackendRefs)
	}
	return indexL4RouteByBackendService(
		ctx,
		logger,
		tcpRoute,
		rulesBackendRefs,
		"tcpRoute",
		tcpRouteBackendServiceIndexKey,
	)
}

func (m *WatchesModel) indexUDPRouteByBackendService(ctx context.Context, obj client.Object) []string {
	udpRoute, isRoute := obj.(*gatewayv1alpha2.UDPRoute)
	logger := m.logger.WithGroup("udp-route-backend-service-index")
	if !isRoute {
		logger.WarnContext(ctx, "Received non-UDPRoute object", slog.Any("object", obj))
		return nil
	}
	if udpRoute.DeletionTimestamp != nil {
		logger.DebugContext(ctx, "Ignoring UDPRoute marked for deletion",
			slog.String("udpRoute", client.ObjectKeyFromObject(udpRoute).String()),
			slog.Time("deletionTimestamp", udpRoute.DeletionTimestamp.Time),
		)
		return nil
	}

	rulesBackendRefs := make([][]gatewayv1.BackendRef, 0, len(udpRoute.Spec.Rules))
	for _, rule := range udpRoute.Spec.Rules {
		rulesBackendRefs = append(rulesBackendRefs, rule.BackendRefs)
	}
	return indexL4RouteByBackendService(
		ctx,
		logger,
		udpRoute,
		rulesBackendRefs,
		"udpRoute",
		udpRouteBackendServiceIndexKey,
	)
}

func indexL4RouteByBackendService(
	ctx context.Context,
	logger *slog.Logger,
	route client.Object,
	rulesBackendRefs [][]gatewayv1.BackendRef,
	routeAttr string,
	indexKey string,
) []string {
	uniqueServiceKeys := make(map[string]struct{})
	for _, backendRefs := range rulesBackendRefs {
		for _, key := range indexBackendRefsByService(route.GetNamespace(), backendRefs) {
			uniqueServiceKeys[key] = struct{}{}
		}
	}

	serviceKeys := lo.Keys(uniqueServiceKeys)
	logger.DebugContext(ctx, "Indexed L4 route by backend service",
		slog.String(routeAttr, client.ObjectKeyFromObject(route).String()),
		slog.String("indexKey", indexKey),
		slog.Any("serviceKeys", serviceKeys),
	)
	return serviceKeys
}

// indexGatewayByCertificateSecrets extracts the namespaced names of Secrets referenced
// in a Gateway's listeners for TLS certificates. This is used to create an index
// for efficient lookup when a Secret changes.
func (m *WatchesModel) indexGatewayByCertificateSecrets(ctx context.Context, obj client.Object) []string {
	gateway, isGateway := obj.(*gatewayv1.Gateway)
	logger := m.logger.WithGroup("gateway-certificate-secret-index")

	if !isGateway {
		logger.WarnContext(ctx, "Received non-Gateway object", slog.Any("object", obj))
		return nil
	}

	if gateway.DeletionTimestamp != nil {
		logger.DebugContext(ctx, "Ignoring Gateway marked for deletion",
			slog.String("gateway", client.ObjectKeyFromObject(gateway).String()),
			slog.Time("deletionTimestamp", gateway.DeletionTimestamp.Time),
		)
		return nil
	}

	if gateway.Annotations == nil || gateway.Annotations[ControllerClassName] != "true" {
		logger.DebugContext(ctx, "Gateway is not accepted by this controller. Skipping indexing",
			slog.String("gateway", client.ObjectKeyFromObject(gateway).String()),
		)
		return nil
	}

	uniqueSecretKeys := make(map[string]struct{})
	for _, listener := range gateway.Spec.Listeners {
		if listener.Protocol != gatewayv1.HTTPSProtocolType || listener.TLS == nil {
			continue
		}

		for _, ref := range listener.TLS.CertificateRefs {
			ns := gateway.Namespace
			if ref.Namespace != nil {
				ns = string(*ref.Namespace)
			}
			namespacedName := path.Join(ns, string(ref.Name))
			if _, ok := uniqueSecretKeys[namespacedName]; !ok {
				uniqueSecretKeys[namespacedName] = struct{}{}
			}
		}
	}

	secretKeys := lo.Keys(uniqueSecretKeys)
	logger.DebugContext(ctx, "Indexed Gateway by certificate",
		slog.String("gateway", client.ObjectKeyFromObject(gateway).String()),
		slog.String("gatewayResourceVersion", gateway.ResourceVersion),
		slog.Int64("gatewayGeneration", gateway.Generation),
		slog.String("indexKey", gatewayCertificateIndexKey),
		slog.Any("secretKeys", secretKeys),
	)

	return secretKeys
}

// MapEndpointSliceToHTTPRoute maps EndpointSlice events to HTTPRoute reconcile requests.
// Its signature matches handler.MapFunc.
func (m *WatchesModel) MapEndpointSliceToHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	epSlice, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		m.logger.WarnContext(ctx, "Received non-EndpointSlice object", slog.Any("object", obj))
		return nil
	}

	svcName, ok := epSlice.Labels[discoveryv1.LabelServiceName]
	if !ok {
		m.logger.WarnContext(ctx, "EndpointSlice missing service name label", slog.Any("endpointSlice", epSlice))
		return nil
	}

	ns := epSlice.Namespace
	indexKey := path.Join(ns, svcName)

	var routeList gatewayv1.HTTPRouteList
	// TODO: Fetch all pages?
	if err := m.k8sClient.List(
		ctx,
		&routeList,
		client.MatchingFields{httpRouteBackendServiceIndexKey: indexKey},
	); err != nil {
		m.logger.ErrorContext(ctx,
			"Failed to list HTTPRoutes for service",
			slog.String("indexKey", indexKey),
			diag.ErrAttr(err),
		)
		return nil
	}

	if len(routeList.Items) == 0 {
		m.logger.DebugContext(
			ctx,
			"No HTTPRoutes found for service",
			slog.String("service", svcName),
			slog.String("indexKey", indexKey),
		)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(routeList.Items))
	for _, route := range routeList.Items {
		if route.DeletionTimestamp != nil {
			m.logger.DebugContext(ctx,
				"Skipping HTTPRoute marked for deletion",
				slog.String("httpRoute", client.ObjectKeyFromObject(&route).String()),
				slog.String("endpointSlice", client.ObjectKeyFromObject(epSlice).String()),
				slog.Time("deletionTimestamp", route.DeletionTimestamp.Time),
			)
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&route),
		})
		m.logger.InfoContext(ctx,
			"Queueing HTTPRoute for reconciliation due to EndpointSlice change",
			slog.String("httpRoute", client.ObjectKeyFromObject(&route).String()),
			slog.String("endpointSlice", client.ObjectKeyFromObject(epSlice).String()),
		)
	}

	return requests
}

func (m *WatchesModel) MapEndpointSliceToGRPCRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	var routeList gatewayv1.GRPCRouteList
	return mapEndpointSliceToL4Route(
		ctx,
		m.logger,
		m.k8sClient,
		obj,
		&routeList,
		grpcRouteBackendServiceIndexKey,
		"GRPCRoutes",
		func(routeList *gatewayv1.GRPCRouteList) []reconcile.Request {
			requests := make([]reconcile.Request, 0, len(routeList.Items))
			for _, route := range routeList.Items {
				if route.DeletionTimestamp != nil {
					continue
				}
				requests = append(requests, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&route),
				})
			}
			return requests
		},
	)
}

func (m *WatchesModel) MapHTTPRouteToGRPCRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	if _, ok := obj.(*gatewayv1.HTTPRoute); !ok {
		m.logger.WarnContext(ctx, "Received non-HTTPRoute object", slog.Any("object", obj))
		return nil
	}

	var routeList gatewayv1.GRPCRouteList
	if err := m.k8sClient.List(ctx, &routeList); err != nil {
		m.logger.ErrorContext(ctx, "Failed to list GRPCRoutes for HTTPRoute change", diag.ErrAttr(err))
		return nil
	}
	return lo.FilterMap(routeList.Items, func(route gatewayv1.GRPCRoute, _ int) (reconcile.Request, bool) {
		if route.DeletionTimestamp != nil {
			return reconcile.Request{}, false
		}
		return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)}, true
	})
}

func (m *WatchesModel) MapGRPCRouteToHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	if _, ok := obj.(*gatewayv1.GRPCRoute); !ok {
		m.logger.WarnContext(ctx, "Received non-GRPCRoute object", slog.Any("object", obj))
		return nil
	}

	var routeList gatewayv1.HTTPRouteList
	if err := m.k8sClient.List(ctx, &routeList); err != nil {
		m.logger.ErrorContext(ctx, "Failed to list HTTPRoutes for GRPCRoute change", diag.ErrAttr(err))
		return nil
	}
	return lo.FilterMap(routeList.Items, func(route gatewayv1.HTTPRoute, _ int) (reconcile.Request, bool) {
		if route.DeletionTimestamp != nil {
			return reconcile.Request{}, false
		}
		return reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)}, true
	})
}

func (m *WatchesModel) MapEndpointSliceToTCPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	var routeList gatewayv1alpha2.TCPRouteList
	return mapEndpointSliceToL4Route(
		ctx,
		m.logger,
		m.k8sClient,
		obj,
		&routeList,
		tcpRouteBackendServiceIndexKey,
		"TCPRoutes",
		func(routeList *gatewayv1alpha2.TCPRouteList) []reconcile.Request {
			requests := make([]reconcile.Request, 0, len(routeList.Items))
			for _, route := range routeList.Items {
				if route.DeletionTimestamp != nil {
					continue
				}
				requests = append(requests, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&route),
				})
			}
			return requests
		},
	)
}

func (m *WatchesModel) MapEndpointSliceToUDPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	var routeList gatewayv1alpha2.UDPRouteList
	return mapEndpointSliceToL4Route(
		ctx,
		m.logger,
		m.k8sClient,
		obj,
		&routeList,
		udpRouteBackendServiceIndexKey,
		"UDPRoutes",
		func(routeList *gatewayv1alpha2.UDPRouteList) []reconcile.Request {
			requests := make([]reconcile.Request, 0, len(routeList.Items))
			for _, route := range routeList.Items {
				if route.DeletionTimestamp != nil {
					continue
				}
				requests = append(requests, reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&route),
				})
			}
			return requests
		},
	)
}

func mapEndpointSliceToL4Route[T client.ObjectList](
	ctx context.Context,
	logger *slog.Logger,
	k8sClient k8sClient,
	obj client.Object,
	routeList T,
	indexKey string,
	routeKind string,
	requestsFromList func(T) []reconcile.Request,
) []reconcile.Request {
	epSlice, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		logger.WarnContext(ctx, "Received non-EndpointSlice object", slog.Any("object", obj))
		return nil
	}

	svcName, ok := epSlice.Labels[discoveryv1.LabelServiceName]
	if !ok {
		logger.WarnContext(ctx, "EndpointSlice missing service name label", slog.Any("endpointSlice", epSlice))
		return nil
	}

	serviceIndexKey := path.Join(epSlice.Namespace, svcName)
	if err := k8sClient.List(
		ctx,
		routeList,
		client.MatchingFields{indexKey: serviceIndexKey},
	); err != nil {
		logger.ErrorContext(ctx,
			fmt.Sprintf("Failed to list %s for service", routeKind),
			slog.String("indexKey", serviceIndexKey),
			diag.ErrAttr(err),
		)
		return nil
	}

	return requestsFromList(routeList)
}

func routeReferencesBackendNamespace(routeNamespace string, refs []gatewayv1.BackendRef, backendNamespace string) bool {
	for _, backendRef := range refs {
		ns := routeNamespace
		if backendRef.BackendObjectReference.Namespace != nil {
			ns = string(*backendRef.BackendObjectReference.Namespace)
		}
		if ns == backendNamespace && ns != routeNamespace {
			return true
		}
	}
	return false
}

func (m *WatchesModel) MapReferenceGrantToTCPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	var routeList gatewayv1alpha2.TCPRouteList
	return mapReferenceGrantToL4Route(ctx, m.logger, m.k8sClient, obj, &routeList, "TCPRoutes",
		func(routeList *gatewayv1alpha2.TCPRouteList, grant *gatewayv1beta1.ReferenceGrant) []reconcile.Request {
			requests := make([]reconcile.Request, 0)
			for _, route := range routeList.Items {
				shouldQueue := false
				for _, rule := range route.Spec.Rules {
					if routeReferencesBackendNamespace(route.Namespace, rule.BackendRefs, grant.Namespace) {
						shouldQueue = true
						break
					}
				}
				if shouldQueue {
					requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)})
				}
			}
			return requests
		},
	)
}

func (m *WatchesModel) MapReferenceGrantToUDPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	var routeList gatewayv1alpha2.UDPRouteList
	return mapReferenceGrantToL4Route(ctx, m.logger, m.k8sClient, obj, &routeList, "UDPRoutes",
		func(routeList *gatewayv1alpha2.UDPRouteList, grant *gatewayv1beta1.ReferenceGrant) []reconcile.Request {
			requests := make([]reconcile.Request, 0)
			for _, route := range routeList.Items {
				shouldQueue := false
				for _, rule := range route.Spec.Rules {
					if routeReferencesBackendNamespace(route.Namespace, rule.BackendRefs, grant.Namespace) {
						shouldQueue = true
						break
					}
				}
				if shouldQueue {
					requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)})
				}
			}
			return requests
		},
	)
}

func mapReferenceGrantToL4Route[T client.ObjectList](
	ctx context.Context,
	logger *slog.Logger,
	k8sClient k8sClient,
	obj client.Object,
	routeList T,
	routeKind string,
	requestsFromList func(T, *gatewayv1beta1.ReferenceGrant) []reconcile.Request,
) []reconcile.Request {
	grant, ok := obj.(*gatewayv1beta1.ReferenceGrant)
	if !ok {
		logger.WarnContext(ctx, "Received non-ReferenceGrant object", slog.Any("object", obj))
		return nil
	}

	if err := k8sClient.List(ctx, routeList); err != nil {
		logger.ErrorContext(ctx, fmt.Sprintf("Failed to list %s for ReferenceGrant change", routeKind),
			slog.String("referenceGrant", client.ObjectKeyFromObject(grant).String()),
			diag.ErrAttr(err),
		)
		return nil
	}

	return requestsFromList(routeList, grant)
}

func tcpRouteReferencesGateway(route gatewayv1alpha2.TCPRoute, gateway gatewayv1.Gateway) bool {
	gatewayName := client.ObjectKeyFromObject(&gateway)
	for _, parentRef := range route.Spec.ParentRefs {
		if !parentRefTargetsGateway(parentRef) {
			continue
		}
		if tcpParentRefTarget(parentRef, route.Namespace) == gatewayName {
			return true
		}
	}
	return false
}

func udpRouteReferencesGateway(route gatewayv1alpha2.UDPRoute, gateway gatewayv1.Gateway) bool {
	gatewayName := client.ObjectKeyFromObject(&gateway)
	for _, parentRef := range route.Spec.ParentRefs {
		if !parentRefTargetsGateway(parentRef) {
			continue
		}
		if udpParentRefTarget(parentRef, route.Namespace) == gatewayName {
			return true
		}
	}
	return false
}

func (m *WatchesModel) MapGatewayToTCPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	var routeList gatewayv1alpha2.TCPRouteList
	return mapGatewayToL4Route(ctx, m.logger, m.k8sClient, obj, &routeList, "TCPRoutes",
		func(routeList *gatewayv1alpha2.TCPRouteList, gateway *gatewayv1.Gateway) []reconcile.Request {
			requests := make([]reconcile.Request, 0)
			for _, route := range routeList.Items {
				if tcpRouteReferencesGateway(route, *gateway) {
					requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)})
				}
			}
			return requests
		},
	)
}

func (m *WatchesModel) MapGatewayToUDPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	var routeList gatewayv1alpha2.UDPRouteList
	return mapGatewayToL4Route(ctx, m.logger, m.k8sClient, obj, &routeList, "UDPRoutes",
		func(routeList *gatewayv1alpha2.UDPRouteList, gateway *gatewayv1.Gateway) []reconcile.Request {
			requests := make([]reconcile.Request, 0)
			for _, route := range routeList.Items {
				if udpRouteReferencesGateway(route, *gateway) {
					requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)})
				}
			}
			return requests
		},
	)
}

func mapGatewayToL4Route[T client.ObjectList](
	ctx context.Context,
	logger *slog.Logger,
	k8sClient k8sClient,
	obj client.Object,
	routeList T,
	routeKind string,
	requestsFromList func(T, *gatewayv1.Gateway) []reconcile.Request,
) []reconcile.Request {
	gateway, ok := obj.(*gatewayv1.Gateway)
	if !ok {
		logger.WarnContext(ctx, "Received non-Gateway object", slog.Any("object", obj))
		return nil
	}

	if err := k8sClient.List(ctx, routeList); err != nil {
		logger.ErrorContext(ctx, fmt.Sprintf("Failed to list %s for Gateway change", routeKind),
			slog.String("gateway", client.ObjectKeyFromObject(gateway).String()),
			diag.ErrAttr(err),
		)
		return nil
	}

	return requestsFromList(routeList, gateway)
}

// MapGatewayConfigToGateway maps GatewayConfig events to Gateway reconcile requests.
// Its signature matches handler.MapFunc.
func (m *WatchesModel) MapGatewayConfigToGateway(ctx context.Context, obj client.Object) []reconcile.Request {
	config, ok := obj.(*configtypes.GatewayConfig)
	if !ok {
		m.logger.WarnContext(ctx, "Received non-GatewayConfig object", slog.Any("object", obj))
		return nil
	}

	var gatewayList gatewayv1.GatewayList
	if err := m.k8sClient.List(ctx, &gatewayList, client.InNamespace(config.Namespace)); err != nil {
		m.logger.ErrorContext(ctx, "Failed to list Gateways for GatewayConfig change",
			slog.String("gatewayConfig", client.ObjectKeyFromObject(config).String()),
			diag.ErrAttr(err),
		)
		return nil
	}

	requests := make([]reconcile.Request, 0)
	for _, gateway := range gatewayList.Items {
		if gateway.DeletionTimestamp != nil ||
			!gatewayUsesSupportedController(&gateway) ||
			gateway.Spec.Infrastructure == nil ||
			gateway.Spec.Infrastructure.ParametersRef == nil ||
			gateway.Spec.Infrastructure.ParametersRef.Name != config.Name {
			continue
		}

		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&gateway)})
		m.logger.InfoContext(ctx,
			"Queueing Gateway for reconciliation due to GatewayConfig change",
			slog.String("gateway", client.ObjectKeyFromObject(&gateway).String()),
			slog.String("gatewayConfig", client.ObjectKeyFromObject(config).String()),
			slog.String("resourceVersion", gateway.ResourceVersion),
			slog.Int64("generation", gateway.Generation),
			slog.String("gatewayConfigResourceVersion", config.ResourceVersion),
			slog.Int64("gatewayConfigGeneration", config.Generation),
		)
	}

	return requests
}

func gatewayUsesSupportedController(gateway *gatewayv1.Gateway) bool {
	if gateway.Annotations == nil {
		return false
	}
	return gateway.Annotations[ControllerClassName] == "true" ||
		gateway.Annotations[NetworkLoadBalancerControllerClassName] == "true"
}

// MapSecretToGateway maps Secret events to Gateway reconcile requests.
// Its signature matches handler.MapFunc.
func (m *WatchesModel) MapSecretToGateway(ctx context.Context, obj client.Object) []reconcile.Request {
	objectKey := client.ObjectKeyFromObject(obj).String()

	secret, ok := obj.(*corev1.Secret)
	if !ok {
		m.logger.WarnContext(ctx, "Received non-Secret object", slog.Any("object", obj))
		return nil
	}

	// Ignore non-TLS secrets
	if secret.Type != corev1.SecretTypeTLS {
		m.logger.DebugContext(ctx, "Ignoring non-TLS secret",
			slog.String("secret", objectKey),
			slog.String("type", string(secret.Type)),
		)
		return nil
	}

	// Verify that the secret has the required TLS data
	if _, hasCert := secret.Data[corev1.TLSCertKey]; !hasCert {
		m.logger.DebugContext(ctx, "Ignoring TLS secret without certificate data",
			slog.String("secret", objectKey),
		)
		return nil
	}
	if _, hasKey := secret.Data[corev1.TLSPrivateKeyKey]; !hasKey {
		m.logger.DebugContext(ctx, "Ignoring TLS secret without private key data",
			slog.String("secret", objectKey),
		)
		return nil
	}

	indexKey := path.Join(secret.Namespace, secret.Name)

	var gatewayList gatewayv1.GatewayList
	if err := m.k8sClient.List(
		ctx,
		&gatewayList,
		client.MatchingFields{gatewayCertificateIndexKey: indexKey},
	); err != nil {
		m.logger.ErrorContext(ctx,
			"Failed to list Gateways for certificate",
			slog.String("indexKey", indexKey),
			diag.ErrAttr(err),
		)
		return nil
	}

	if len(gatewayList.Items) == 0 {
		m.logger.DebugContext(
			ctx,
			"No Gateways found for certificate",
			slog.String("secret", secret.Name),
			slog.String("indexKey", indexKey),
		)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(gatewayList.Items))
	for _, gateway := range gatewayList.Items {
		if gateway.DeletionTimestamp != nil {
			m.logger.DebugContext(ctx,
				"Skipping Gateway marked for deletion",
				slog.String("gateway", client.ObjectKeyFromObject(&gateway).String()),
				slog.String("secret", objectKey),
				slog.Time("deletionTimestamp", gateway.DeletionTimestamp.Time),
			)
			continue
		}
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&gateway),
		})
		m.logger.InfoContext(ctx,
			"Queueing Gateway for reconciliation due to Secret change",
			slog.String("gateway", client.ObjectKeyFromObject(&gateway).String()),
			slog.String("resourceVersion", gateway.ResourceVersion),
			slog.Int64("generation", gateway.Generation),
			slog.String("secret", objectKey),
			slog.String("secretResourceVersion", secret.ResourceVersion),
			slog.Int64("secretGeneration", secret.Generation),
		)
	}

	return requests
}

// Note: indexHTTPRouteByBackendService and httpRouteBackendServiceIndexKey removed as part of stubbing.
