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
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	configtypes "github.com/gemyago/oke-gateway-api/internal/types"
)

const httpRouteBackendServiceIndexKey = ".metadata.backendRefs.serviceName" // Virtual field name, indexed
const grpcRouteBackendServiceIndexKey = ".metadata.grpcBackendRefs.serviceName"
const httpRouteParentGatewayIndexKey = ".metadata.parentRefs.gateway"
const grpcRouteParentGatewayIndexKey = ".metadata.grpcParentRefs.gateway"
const tlsRouteParentGatewayIndexKey = ".metadata.tlsParentRefs.gateway"
const gatewayCertificateIndexKey = ".metadata.certificates" // Virtual field name, indexed
const tcpRouteBackendServiceIndexKey = ".metadata.tcpBackendRefs.serviceName"
const udpRouteBackendServiceIndexKey = ".metadata.udpBackendRefs.serviceName"
const tlsRouteBackendServiceIndexKey = ".metadata.tlsBackendRefs.serviceName"

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
	EnableTLSRoute bool
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

	if err := m.registerL7RouteParentGatewayIndexers(ctx, indexer); err != nil {
		return err
	}

	if opts.EnableTCPRoute {
		if err := indexer.IndexField(ctx,
			&gatewayv1.TCPRoute{},
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
			&gatewayv1.UDPRoute{},
			udpRouteBackendServiceIndexKey,
			func(o client.Object) []string {
				return m.indexUDPRouteByBackendService(ctx, o)
			},
		); err != nil {
			return fmt.Errorf("failed to index UDPRoute by backend service: %w", err)
		}
	}

	if opts.EnableTLSRoute {
		if err := m.registerTLSRouteIndexers(ctx, indexer); err != nil {
			return err
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
		slog.String("indexKey", httpRouteParentGatewayIndexKey),
		slog.String("indexKey", grpcRouteParentGatewayIndexKey),
		slog.String("indexKey", tlsRouteParentGatewayIndexKey),
		slog.String("indexKey", gatewayCertificateIndexKey),
		slog.Bool("tcpRouteIndexEnabled", opts.EnableTCPRoute),
		slog.Bool("udpRouteIndexEnabled", opts.EnableUDPRoute),
		slog.Bool("tlsRouteIndexEnabled", opts.EnableTLSRoute),
	)
	return nil
}

func (m *WatchesModel) registerL7RouteParentGatewayIndexers(
	ctx context.Context,
	indexer client.FieldIndexer,
) error {
	if err := indexer.IndexField(ctx,
		&gatewayv1.HTTPRoute{},
		httpRouteParentGatewayIndexKey,
		func(o client.Object) []string {
			return m.indexHTTPRouteByParentGateway(ctx, o)
		},
	); err != nil {
		return fmt.Errorf("failed to index HTTPRoute by parent Gateway: %w", err)
	}

	if err := indexer.IndexField(ctx,
		&gatewayv1.GRPCRoute{},
		grpcRouteParentGatewayIndexKey,
		func(o client.Object) []string {
			return m.indexGRPCRouteByParentGateway(ctx, o)
		},
	); err != nil {
		return fmt.Errorf("failed to index GRPCRoute by parent Gateway: %w", err)
	}
	return nil
}

func (m *WatchesModel) registerTLSRouteIndexers(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx,
		&gatewayv1.TLSRoute{},
		tlsRouteBackendServiceIndexKey,
		func(o client.Object) []string {
			return m.indexTLSRouteByBackendService(ctx, o)
		},
	); err != nil {
		return fmt.Errorf("failed to index TLSRoute by backend service: %w", err)
	}
	if err := indexer.IndexField(ctx,
		&gatewayv1.TLSRoute{},
		tlsRouteParentGatewayIndexKey,
		func(o client.Object) []string {
			return m.indexTLSRouteByParentGateway(ctx, o)
		},
	); err != nil {
		return fmt.Errorf("failed to index TLSRoute by parent Gateway: %w", err)
	}
	return nil
}

func (m *WatchesModel) indexHTTPRouteByParentGateway(ctx context.Context, obj client.Object) []string {
	httpRoute, isRoute := obj.(*gatewayv1.HTTPRoute)
	if !isRoute {
		m.logger.WarnContext(ctx, "Received non-HTTPRoute object", slog.Any("object", obj))
		return nil
	}
	if httpRoute.DeletionTimestamp != nil {
		return nil
	}
	return parentGatewayIndexKeys(httpRoute.Namespace, httpRoute.Spec.ParentRefs)
}

func (m *WatchesModel) indexGRPCRouteByParentGateway(ctx context.Context, obj client.Object) []string {
	grpcRoute, isRoute := obj.(*gatewayv1.GRPCRoute)
	if !isRoute {
		m.logger.WarnContext(ctx, "Received non-GRPCRoute object", slog.Any("object", obj))
		return nil
	}
	if grpcRoute.DeletionTimestamp != nil {
		return nil
	}
	return parentGatewayIndexKeys(grpcRoute.Namespace, grpcRoute.Spec.ParentRefs)
}

func (m *WatchesModel) indexTLSRouteByParentGateway(ctx context.Context, obj client.Object) []string {
	tlsRoute, isRoute := obj.(*gatewayv1.TLSRoute)
	if !isRoute {
		m.logger.WarnContext(ctx, "Received non-TLSRoute object", slog.Any("object", obj))
		return nil
	}
	if tlsRoute.DeletionTimestamp != nil {
		return nil
	}
	return parentGatewayIndexKeys(tlsRoute.Namespace, tlsRoute.Spec.ParentRefs)
}

func parentGatewayIndexKeys(routeNamespace string, refs []gatewayv1.ParentReference) []string {
	uniqueGatewayKeys := make(map[string]struct{})
	for _, ref := range refs {
		if ref.Group != nil && string(*ref.Group) != gatewayv1.GroupName {
			continue
		}
		if ref.Kind != nil && string(*ref.Kind) != "Gateway" {
			continue
		}

		namespace := routeNamespace
		if ref.Namespace != nil {
			namespace = string(*ref.Namespace)
		}
		uniqueGatewayKeys[path.Join(namespace, string(ref.Name))] = struct{}{}
	}
	return lo.Keys(uniqueGatewayKeys)
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
	tcpRoute, isRoute := obj.(*gatewayv1.TCPRoute)
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
	udpRoute, isRoute := obj.(*gatewayv1.UDPRoute)
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

func (m *WatchesModel) indexTLSRouteByBackendService(ctx context.Context, obj client.Object) []string {
	tlsRoute, isRoute := obj.(*gatewayv1.TLSRoute)
	logger := m.logger.WithGroup("tls-route-backend-service-index")
	if !isRoute {
		logger.WarnContext(ctx, "Received non-TLSRoute object", slog.Any("object", obj))
		return nil
	}
	if tlsRoute.DeletionTimestamp != nil {
		logger.DebugContext(ctx, "Ignoring TLSRoute marked for deletion",
			slog.String("tlsRoute", client.ObjectKeyFromObject(tlsRoute).String()),
			slog.Time("deletionTimestamp", tlsRoute.DeletionTimestamp.Time),
		)
		return nil
	}

	rulesBackendRefs := make([][]gatewayv1.BackendRef, 0, len(tlsRoute.Spec.Rules))
	for _, rule := range tlsRoute.Spec.Rules {
		rulesBackendRefs = append(rulesBackendRefs, rule.BackendRefs)
	}
	return indexL4RouteByBackendService(
		ctx,
		logger,
		tlsRoute,
		rulesBackendRefs,
		"tlsRoute",
		tlsRouteBackendServiceIndexKey,
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
		if (listener.Protocol != gatewayv1.HTTPSProtocolType && listener.Protocol != gatewayv1.TLSProtocolType) ||
			listener.TLS == nil {
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
	httpRoute, ok := obj.(*gatewayv1.HTTPRoute)
	if !ok {
		m.logger.WarnContext(ctx, "Received non-HTTPRoute object", slog.Any("object", obj))
		return nil
	}

	return mapParentGatewaysToL7RouteRequests(
		ctx,
		m.logger,
		m.k8sClient,
		parentGatewayIndexKeys(httpRoute.Namespace, httpRoute.Spec.ParentRefs),
		grpcRouteParentGatewayIndexKey,
		"GRPCRoutes",
		func() *gatewayv1.GRPCRouteList { return &gatewayv1.GRPCRouteList{} },
		func(routeList *gatewayv1.GRPCRouteList, requestsByKey map[client.ObjectKey]reconcile.Request) {
			for _, route := range routeList.Items {
				if route.DeletionTimestamp != nil {
					continue
				}
				key := client.ObjectKeyFromObject(&route)
				requestsByKey[key] = reconcile.Request{NamespacedName: key}
			}
		},
	)
}

func mapParentGatewaysToL7RouteRequests[T client.ObjectList](
	ctx context.Context,
	logger *slog.Logger,
	k8sClient k8sClient,
	parentGatewayKeys []string,
	indexKey string,
	routeKind string,
	newRouteList func() T,
	appendRequests func(T, map[client.ObjectKey]reconcile.Request),
) []reconcile.Request {
	requestsByKey := make(map[client.ObjectKey]reconcile.Request)
	for _, parentGatewayKey := range parentGatewayKeys {
		routeList := newRouteList()
		if err := k8sClient.List(
			ctx,
			routeList,
			client.MatchingFields{indexKey: parentGatewayKey},
		); err != nil {
			logger.ErrorContext(ctx, fmt.Sprintf("Failed to list %s for parent Gateway change", routeKind),
				slog.String("indexKey", parentGatewayKey),
				diag.ErrAttr(err))
			return nil
		}
		appendRequests(routeList, requestsByKey)
	}
	return lo.Values(requestsByKey)
}

func (m *WatchesModel) MapGRPCRouteToHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	grpcRoute, ok := obj.(*gatewayv1.GRPCRoute)
	if !ok {
		m.logger.WarnContext(ctx, "Received non-GRPCRoute object", slog.Any("object", obj))
		return nil
	}

	return mapParentGatewaysToL7RouteRequests(
		ctx,
		m.logger,
		m.k8sClient,
		parentGatewayIndexKeys(grpcRoute.Namespace, grpcRoute.Spec.ParentRefs),
		httpRouteParentGatewayIndexKey,
		"HTTPRoutes",
		func() *gatewayv1.HTTPRouteList { return &gatewayv1.HTTPRouteList{} },
		func(routeList *gatewayv1.HTTPRouteList, requestsByKey map[client.ObjectKey]reconcile.Request) {
			for _, route := range routeList.Items {
				if route.DeletionTimestamp != nil {
					continue
				}
				key := client.ObjectKeyFromObject(&route)
				requestsByKey[key] = reconcile.Request{NamespacedName: key}
			}
		},
	)
}

func (m *WatchesModel) MapEndpointSliceToTCPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	var routeList gatewayv1.TCPRouteList
	return mapEndpointSliceToL4Route(
		ctx,
		m.logger,
		m.k8sClient,
		obj,
		&routeList,
		tcpRouteBackendServiceIndexKey,
		"TCPRoutes",
		func(routeList *gatewayv1.TCPRouteList) []reconcile.Request {
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
	var routeList gatewayv1.UDPRouteList
	return mapEndpointSliceToL4Route(
		ctx,
		m.logger,
		m.k8sClient,
		obj,
		&routeList,
		udpRouteBackendServiceIndexKey,
		"UDPRoutes",
		func(routeList *gatewayv1.UDPRouteList) []reconcile.Request {
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

func (m *WatchesModel) MapEndpointSliceToTLSRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	var routeList gatewayv1.TLSRouteList
	return mapEndpointSliceToL4Route(
		ctx,
		m.logger,
		m.k8sClient,
		obj,
		&routeList,
		tlsRouteBackendServiceIndexKey,
		"TLSRoutes",
		func(routeList *gatewayv1.TLSRouteList) []reconcile.Request {
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

func (m *WatchesModel) MapBackendTLSPolicyToHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	return m.mapBackendTLSPolicyToIndexedRoutes(ctx, obj, &gatewayv1.HTTPRouteList{},
		httpRouteBackendServiceIndexKey, "HTTPRoutes")
}

func (m *WatchesModel) MapBackendTLSPolicyToGRPCRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	return m.mapBackendTLSPolicyToIndexedRoutes(ctx, obj, &gatewayv1.GRPCRouteList{},
		grpcRouteBackendServiceIndexKey, "GRPCRoutes")
}

func (m *WatchesModel) MapBackendTLSPolicyToTLSRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	return m.mapBackendTLSPolicyToIndexedRoutes(ctx, obj, &gatewayv1.TLSRouteList{},
		tlsRouteBackendServiceIndexKey, "TLSRoutes")
}

func (m *WatchesModel) MapConfigMapToHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	return m.mapConfigMapToBackendTLSPolicyRoutes(
		ctx,
		obj,
		func(policy gatewayv1.BackendTLSPolicy) []reconcile.Request {
			return m.MapBackendTLSPolicyToHTTPRoute(ctx, &policy)
		},
	)
}

func (m *WatchesModel) MapConfigMapToGRPCRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	return m.mapConfigMapToBackendTLSPolicyRoutes(
		ctx,
		obj,
		func(policy gatewayv1.BackendTLSPolicy) []reconcile.Request {
			return m.MapBackendTLSPolicyToGRPCRoute(ctx, &policy)
		},
	)
}

func (m *WatchesModel) MapConfigMapToTLSRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	return m.mapConfigMapToBackendTLSPolicyRoutes(
		ctx,
		obj,
		func(policy gatewayv1.BackendTLSPolicy) []reconcile.Request {
			return m.MapBackendTLSPolicyToTLSRoute(ctx, &policy)
		},
	)
}

func (m *WatchesModel) MapServiceToHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	return m.mapServiceToIndexedRoutes(ctx, obj, &gatewayv1.HTTPRouteList{},
		httpRouteBackendServiceIndexKey, "HTTPRoutes")
}

func (m *WatchesModel) MapServiceToGRPCRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	return m.mapServiceToIndexedRoutes(ctx, obj, &gatewayv1.GRPCRouteList{},
		grpcRouteBackendServiceIndexKey, "GRPCRoutes")
}

func (m *WatchesModel) MapServiceToTLSRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	return m.mapServiceToIndexedRoutes(ctx, obj, &gatewayv1.TLSRouteList{},
		tlsRouteBackendServiceIndexKey, "TLSRoutes")
}

func (m *WatchesModel) mapBackendTLSPolicyToIndexedRoutes(
	ctx context.Context,
	obj client.Object,
	routeList client.ObjectList,
	indexKey string,
	routeKind string,
) []reconcile.Request {
	policy, ok := obj.(*gatewayv1.BackendTLSPolicy)
	if !ok {
		m.logger.WarnContext(ctx, "Received non-BackendTLSPolicy object", slog.Any("object", obj))
		return nil
	}
	requestsByKey := make(map[client.ObjectKey]reconcile.Request)
	for _, targetRef := range policy.Spec.TargetRefs {
		if targetRef.Group != "" || targetRef.Kind != "Service" {
			continue
		}
		serviceKey := path.Join(policy.Namespace, string(targetRef.Name))
		if err := m.k8sClient.List(ctx, routeList, client.MatchingFields{indexKey: serviceKey}); err != nil {
			m.logger.ErrorContext(ctx, fmt.Sprintf("Failed to list %s for BackendTLSPolicy change", routeKind),
				slog.String("service", serviceKey),
				diag.ErrAttr(err))
			return nil
		}
		for _, route := range objectListItems(routeList) {
			if route.GetDeletionTimestamp() != nil {
				continue
			}
			key := client.ObjectKeyFromObject(route)
			requestsByKey[key] = reconcile.Request{NamespacedName: key}
		}
	}
	return lo.Values(requestsByKey)
}

func (m *WatchesModel) mapServiceToIndexedRoutes(
	ctx context.Context,
	obj client.Object,
	routeList client.ObjectList,
	indexKey string,
	routeKind string,
) []reconcile.Request {
	service, ok := obj.(*corev1.Service)
	if !ok {
		m.logger.WarnContext(ctx, "Received non-Service object", slog.Any("object", obj))
		return nil
	}
	serviceKey := path.Join(service.Namespace, service.Name)
	return m.mapServiceKeyToIndexedRoutes(ctx, serviceKey, routeList, indexKey, routeKind)
}

func (m *WatchesModel) mapServiceKeyToIndexedRoutes(
	ctx context.Context,
	serviceKey string,
	routeList client.ObjectList,
	indexKey string,
	routeKind string,
) []reconcile.Request {
	if err := m.k8sClient.List(ctx, routeList, client.MatchingFields{indexKey: serviceKey}); err != nil {
		m.logger.ErrorContext(ctx, fmt.Sprintf("Failed to list %s for Service change", routeKind),
			slog.String("service", serviceKey),
			diag.ErrAttr(err))
		return nil
	}
	requestsByKey := make(map[client.ObjectKey]reconcile.Request)
	for _, route := range objectListItems(routeList) {
		if route.GetDeletionTimestamp() != nil {
			continue
		}
		key := client.ObjectKeyFromObject(route)
		requestsByKey[key] = reconcile.Request{NamespacedName: key}
	}
	return lo.Values(requestsByKey)
}

func (m *WatchesModel) mapConfigMapToBackendTLSPolicyRoutes(
	ctx context.Context,
	obj client.Object,
	mapPolicy func(gatewayv1.BackendTLSPolicy) []reconcile.Request,
) []reconcile.Request {
	configMap, ok := obj.(*corev1.ConfigMap)
	if !ok {
		m.logger.WarnContext(ctx, "Received non-ConfigMap object", slog.Any("object", obj))
		return nil
	}
	var policyList gatewayv1.BackendTLSPolicyList
	if err := m.k8sClient.List(ctx, &policyList, client.InNamespace(configMap.Namespace)); err != nil {
		m.logger.ErrorContext(ctx, "Failed to list BackendTLSPolicies for ConfigMap change",
			slog.String("configMap", client.ObjectKeyFromObject(configMap).String()),
			diag.ErrAttr(err))
		return nil
	}
	requestsByKey := make(map[client.ObjectKey]reconcile.Request)
	for _, policy := range policyList.Items {
		if !backendTLSPolicyReferencesConfigMap(policy, configMap.Name) {
			continue
		}
		for _, request := range mapPolicy(policy) {
			requestsByKey[request.NamespacedName] = request
		}
	}
	return lo.Values(requestsByKey)
}

func objectListItems(list client.ObjectList) []client.Object {
	switch typed := list.(type) {
	case *gatewayv1.HTTPRouteList:
		return lo.Map(typed.Items, func(item gatewayv1.HTTPRoute, _ int) client.Object { return &item })
	case *gatewayv1.GRPCRouteList:
		return lo.Map(typed.Items, func(item gatewayv1.GRPCRoute, _ int) client.Object { return &item })
	case *gatewayv1.TLSRouteList:
		return lo.Map(typed.Items, func(item gatewayv1.TLSRoute, _ int) client.Object { return &item })
	default:
		return nil
	}
}

func backendTLSPolicyReferencesConfigMap(policy gatewayv1.BackendTLSPolicy, name string) bool {
	for _, caRef := range policy.Spec.Validation.CACertificateRefs {
		if caRef.Group == "" && caRef.Kind == "ConfigMap" && string(caRef.Name) == name {
			return true
		}
	}
	return false
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
	var routeList gatewayv1.TCPRouteList
	return mapReferenceGrantToL4Route(ctx, m.logger, m.k8sClient, obj, &routeList, "TCPRoutes",
		func(routeList *gatewayv1.TCPRouteList, grant *gatewayv1beta1.ReferenceGrant) []reconcile.Request {
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
	var routeList gatewayv1.UDPRouteList
	return mapReferenceGrantToL4Route(ctx, m.logger, m.k8sClient, obj, &routeList, "UDPRoutes",
		func(routeList *gatewayv1.UDPRouteList, grant *gatewayv1beta1.ReferenceGrant) []reconcile.Request {
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

func (m *WatchesModel) MapReferenceGrantToTLSRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	var routeList gatewayv1.TLSRouteList
	return mapReferenceGrantToL4Route(ctx, m.logger, m.k8sClient, obj, &routeList, "TLSRoutes",
		func(routeList *gatewayv1.TLSRouteList, grant *gatewayv1beta1.ReferenceGrant) []reconcile.Request {
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

func tcpRouteReferencesGateway(route gatewayv1.TCPRoute, gateway gatewayv1.Gateway) bool {
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

func udpRouteReferencesGateway(route gatewayv1.UDPRoute, gateway gatewayv1.Gateway) bool {
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
	var routeList gatewayv1.TCPRouteList
	return mapGatewayToL4Route(ctx, m.logger, m.k8sClient, obj, &routeList, "TCPRoutes",
		func(routeList *gatewayv1.TCPRouteList, gateway *gatewayv1.Gateway) []reconcile.Request {
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
	var routeList gatewayv1.UDPRouteList
	return mapGatewayToL4Route(ctx, m.logger, m.k8sClient, obj, &routeList, "UDPRoutes",
		func(routeList *gatewayv1.UDPRouteList, gateway *gatewayv1.Gateway) []reconcile.Request {
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

func (m *WatchesModel) MapGatewayToTLSRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	gateway, ok := obj.(*gatewayv1.Gateway)
	if !ok {
		m.logger.WarnContext(ctx, "Received non-Gateway object", slog.Any("object", obj))
		return nil
	}

	var routeList gatewayv1.TLSRouteList
	gatewayIndexKey := path.Join(gateway.Namespace, gateway.Name)
	if err := m.k8sClient.List(
		ctx,
		&routeList,
		client.MatchingFields{tlsRouteParentGatewayIndexKey: gatewayIndexKey},
	); err != nil {
		m.logger.ErrorContext(ctx, "Failed to list TLSRoutes for Gateway change",
			slog.String("gateway", client.ObjectKeyFromObject(gateway).String()),
			slog.String("indexKey", gatewayIndexKey),
			diag.ErrAttr(err),
		)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(routeList.Items))
	for _, route := range routeList.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&route)})
	}
	return requests
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

func (m *WatchesModel) MapSecretToTLSRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	gatewayRequests := m.MapSecretToGateway(ctx, obj)
	if len(gatewayRequests) == 0 {
		return nil
	}

	requestsByKey := make(map[client.ObjectKey]reconcile.Request)
	for _, gatewayRequest := range gatewayRequests {
		var gateway gatewayv1.Gateway
		if err := m.k8sClient.Get(ctx, gatewayRequest.NamespacedName, &gateway); err != nil {
			m.logger.ErrorContext(ctx,
				"Failed to get Gateway for TLSRoute Secret mapping",
				slog.String("gateway", gatewayRequest.NamespacedName.String()),
				diag.ErrAttr(err),
			)
			continue
		}
		for _, request := range m.MapGatewayToTLSRoute(ctx, &gateway) {
			requestsByKey[request.NamespacedName] = request
		}
	}
	return lo.Values(requestsByKey)
}

// Note: indexHTTPRouteByBackendService and httpRouteBackendServiceIndexKey removed as part of stubbing.
