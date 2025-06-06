package app

import (
	"context"
	"fmt"
	"log/slog"
	"path"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/samber/lo"
	"go.uber.org/dig"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const httpRouteBackendServiceIndexKey = ".metadata.backendRefs.serviceName" // Virtual field name, indexed
const gatewayCertificateIndexKey = ".metadata.certificates"                 // Virtual field name, indexed

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
		func(o client.Object) []string {
			return m.indexHTTPRouteByBackendService(ctx, o)
		},
	); err != nil {
		return fmt.Errorf("failed to index HTTPRoute by backend service: %w", err)
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
		slog.String("indexKey", gatewayCertificateIndexKey),
	)
	return nil
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
