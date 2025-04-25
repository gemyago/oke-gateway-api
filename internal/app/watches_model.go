package app

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/dig"
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

// RegisterFieldIndexers registers the indexer for HTTPRoute backend service references.
// TODO: Implement index registration logic.
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
	// panic("not implemented") // TODO: Implement
	return nil // Stub implementation
}

// MapEndpointSliceToHTTPRoute maps EndpointSlice events to HTTPRoute reconcile requests.
// Its signature matches handler.MapFunc.
// TODO: Implement mapping logic using index.
func (m *WatchesModel) MapEndpointSliceToHTTPRoute(ctx context.Context, obj client.Object) []reconcile.Request {
	m.logger.InfoContext(ctx,
		"MapEndpointSliceToHTTPRoute called (not implemented)",
		slog.Any("obj", client.ObjectKeyFromObject(obj)),
	)
	// panic("not implemented") // TODO: Implement
	return nil // Stub implementation
}

// Note: indexHTTPRouteByBackendService and httpRouteBackendServiceIndexKey removed as part of stubbing.
