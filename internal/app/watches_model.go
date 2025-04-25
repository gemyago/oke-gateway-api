package app

import (
	"context"
	"fmt"
	"log/slog"
	"path"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/samber/lo"
	"go.uber.org/dig"
	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const httpRouteBackendServiceIndexKey = ".metadata.backendRefs.serviceName"

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

// NewWatchesModel creates a new watchesModel.
func NewWatchesModel(deps WatchesModelDeps) *WatchesModel {
	return &WatchesModel{
		k8sClient: deps.K8sClient,
		logger:    deps.Logger.WithGroup("watches-model"),
	}
}

// RegisterFieldIndexers registers the indexers for the watches model.
func (m *WatchesModel) RegisterFieldIndexers(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx,
		&gatewayv1.HTTPRoute{},
		httpRouteBackendServiceIndexKey,
		m.indexHTTPRouteByBackendService,
	); err != nil {
		return fmt.Errorf("failed to index HTTPRoute by backend service: %w", err)
	}
	return nil
}

// indexHTTPRouteByBackendService extracts the namespaced names of Services referenced
// in an HTTPRoute's backendRefs. This is used to create an index for efficient
// lookup when an EndpointSlice changes.
func (m *WatchesModel) indexHTTPRouteByBackendService(obj client.Object) []string {
	httpRoute, isRoute := obj.(*gatewayv1.HTTPRoute)
	if !isRoute {
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

	return lo.Keys(uniqueServiceKeys)
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

	m.logger.DebugContext(ctx, "Looking for HTTPRoutes referencing service", slog.String("indexKey", indexKey))

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

	m.logger.DebugContext(
		ctx,
		"Found HTTPRoutes for service",
		slog.String("indexKey", indexKey),
		slog.Int("count", len(routeList.Items)),
	)
	requests := make([]reconcile.Request, len(routeList.Items))
	for i, route := range routeList.Items {
		requests[i] = reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&route),
		}
		m.logger.InfoContext(ctx,
			"Queueing HTTPRoute for reconciliation due to EndpointSlice change",
			slog.String("httpRoute", client.ObjectKeyFromObject(&route).String()),
			slog.String("endpointSlice", client.ObjectKeyFromObject(epSlice).String()),
		)
	}

	return requests
}

// Note: indexHTTPRouteByBackendService and httpRouteBackendServiceIndexKey removed as part of stubbing.
