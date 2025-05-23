package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gemyago/oke-gateway-api/internal/types"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	discoveryv1 "k8s.io/api/discovery/v1"
	client "sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type syncRouteEndpointsParams struct {
	httpRoute gatewayv1.HTTPRoute
	config    types.GatewayConfig
}

type syncRouteBackendRefEndpointsParams struct {
	config     types.GatewayConfig
	httpRoute  gatewayv1.HTTPRoute
	backendRef gatewayv1.HTTPBackendRef
}

type identifyBackendsToUpdateParams struct {
	endpointPort    int32
	currentBackends []loadbalancer.Backend
	endpointSlices  []discoveryv1.EndpointSlice
}

type identifyBackendsToUpdateResult struct {
	updateRequired  bool
	updatedBackends []loadbalancer.BackendDetails
	drainingCount   int
}

// httpBackendModel defines the interface for managing OCI backend sets based on HTTPRoute definitions.
type httpBackendModel interface {
	// syncRouteEndpoints synchronizes the OCI Load Balancer Backend Sets associated with the
	// provided HTTPRoute, ensuring they contain the correct set of ready endpoints
	// derived from the referenced Kubernetes Services' EndpointSlices.
	syncRouteEndpoints(ctx context.Context, params syncRouteEndpointsParams) error

	// identifyBackendsToUpdate identifies the backends that need to be updated in the OCI Load Balancer Backend Set.
	// It will correctly handle endpoint status changes, including draining endpoints.
	identifyBackendsToUpdate(
		ctx context.Context,
		params identifyBackendsToUpdateParams,
	) (identifyBackendsToUpdateResult, error)

	// syncRouteBackendRefEndpoints synchronizes the OCI Load Balancer Backend Sets associated with the
	// single backend ref of the provided HTTPRoute.
	syncRouteBackendRefEndpoints(ctx context.Context, params syncRouteBackendRefEndpointsParams) error
}

type httpBackendModelImpl struct {
	logger              *slog.Logger
	k8sClient           k8sClient
	ociClient           ociLoadBalancerClient
	workRequestsWatcher workRequestsWatcher

	// Used to allow mocking own methods in tests
	self httpBackendModel
}

func (m *httpBackendModelImpl) syncRouteEndpoints(
	ctx context.Context,
	params syncRouteEndpointsParams,
) error {
	m.logger.InfoContext(ctx, "Syncing backend endpoints",
		slog.String("httpRoute", params.httpRoute.Name),
		slog.String("config", params.config.Name),
	)

	processedBackendRefs := make(map[string]bool)

	for index, rule := range params.httpRoute.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			refNamespace := lo.Ternary(
				backendRef.Namespace != nil,
				string(lo.FromPtr(backendRef.Namespace)),
				params.httpRoute.Namespace,
			)
			refKey := refNamespace + "/" + string(backendRef.Name)

			if _, ok := processedBackendRefs[refKey]; ok {
				continue
			}

			if err := m.self.syncRouteBackendRefEndpoints(ctx, syncRouteBackendRefEndpointsParams{
				httpRoute:  params.httpRoute,
				config:     params.config,
				backendRef: backendRef,
			}); err != nil {
				return fmt.Errorf("failed to sync route backend endpoints for rule %d: %w", index, err)
			}
			processedBackendRefs[refKey] = true
		}
	}

	return nil
}

func (m *httpBackendModelImpl) identifyBackendsToUpdate(
	ctx context.Context,
	params identifyBackendsToUpdateParams,
) (identifyBackendsToUpdateResult, error) {
	desiredBackendsMap := make(map[string]loadbalancer.BackendDetails)
	var drainingCount int

	for _, slice := range params.endpointSlices {
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
				continue
			}
			if len(endpoint.Addresses) == 0 {
				m.logger.WarnContext(ctx, "Endpoint has no addresses", slog.Any("endpoint", endpoint))
				continue
			}
			ipAddress := endpoint.Addresses[0]
			isDraining := endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating

			if isDraining {
				drainingCount++
			}

			desiredBackendsMap[ipAddress] = loadbalancer.BackendDetails{
				Port:      lo.ToPtr(int(params.endpointPort)),
				IpAddress: &ipAddress,
				Drain:     lo.ToPtr(isDraining),
				// Weight, MaxConnections, Backup, Offline are not managed here
			}
		}
	}

	currentBackendsMap := lo.SliceToMap(
		lo.Filter(params.currentBackends, func(b loadbalancer.Backend, _ int) bool {
			return b.IpAddress != nil
		}),
		func(b loadbalancer.Backend) (string, loadbalancer.Backend) {
			return *b.IpAddress, b
		},
	)

	updateRequired := false
	if len(desiredBackendsMap) != len(currentBackendsMap) {
		updateRequired = true
	} else {
		for ip, desired := range desiredBackendsMap {
			current, exists := currentBackendsMap[ip]
			if !exists || lo.FromPtr(desired.Drain) != lo.FromPtr(current.Drain) {
				updateRequired = true
				break
			}
		}
	}

	updatedBackends := lo.Values(desiredBackendsMap)

	return identifyBackendsToUpdateResult{
		updateRequired:  updateRequired,
		updatedBackends: updatedBackends,
		drainingCount:   drainingCount,
	}, nil
}

func (m *httpBackendModelImpl) syncRouteBackendRefEndpoints(
	ctx context.Context,
	params syncRouteBackendRefEndpointsParams,
) error {
	backendRef := params.backendRef
	backendRefNamespace := lo.Ternary(
		backendRef.Namespace != nil,
		string(lo.FromPtr(backendRef.Namespace)),
		params.httpRoute.Namespace,
	)
	backendSetName := ociBackendSetNameFromBackendRef(params.httpRoute, backendRef)

	getResp, err := m.ociClient.GetBackendSet(ctx, loadbalancer.GetBackendSetRequest{
		LoadBalancerId: &params.config.Spec.LoadBalancerID,
		BackendSetName: &backendSetName,
	})
	if err != nil {
		return fmt.Errorf("failed to get backend set %s: %w", backendSetName, err)
	}
	existingBackendSet := getResp.BackendSet

	backendPort := lo.FromPtr(params.backendRef.BackendObjectReference.Port)

	var endpointSlices discoveryv1.EndpointSliceList

	if err = m.k8sClient.List(ctx, &endpointSlices,
		client.MatchingLabels{
			discoveryv1.LabelServiceName: string(backendRef.BackendObjectReference.Name),
		},
		client.InNamespace(backendRefNamespace),
	); err != nil {
		return fmt.Errorf("failed to list endpoint slices for backend %s: %w", backendRef.BackendObjectReference.Name, err)
	}

	backendsToUpdate, err := m.self.identifyBackendsToUpdate(ctx, identifyBackendsToUpdateParams{
		endpointPort:    int32(backendPort),
		currentBackends: existingBackendSet.Backends,
		endpointSlices:  endpointSlices.Items,
	})
	if err != nil {
		return fmt.Errorf("failed to identify backends to update: %w", err)
	}

	if !backendsToUpdate.updateRequired {
		m.logger.InfoContext(ctx, "Backend set already up-to-date, skipping update",
			slog.String("backendSetName", backendSetName),
			slog.String("httpRoute", params.httpRoute.Name),
			slog.String("backendRefName", string(backendRef.Name)),
			slog.String("backendRefNamespace", backendRefNamespace),
		)
		return nil
	}

	m.logger.InfoContext(ctx, "Syncing backend endpoints for backendRef",
		slog.String("httpRoute", params.httpRoute.Name),
		slog.String("backendRefName", string(backendRef.Name)),
		slog.String("backendRefNamespace", backendRefNamespace),
		slog.String("backendSetName", backendSetName),
		slog.Int("currentBackends", len(existingBackendSet.Backends)),
		slog.Int("updatedBackends", len(backendsToUpdate.updatedBackends)),
		slog.Int("drainingCount", backendsToUpdate.drainingCount),
	)

	ociUpdateResp, err := m.ociClient.UpdateBackendSet(ctx, loadbalancer.UpdateBackendSetRequest{
		LoadBalancerId:          &params.config.Spec.LoadBalancerID,
		BackendSetName:          &backendSetName,
		UpdateBackendSetDetails: makeUpdateOciBackendSetDetails(existingBackendSet, backendsToUpdate.updatedBackends),
	})
	if err != nil {
		return fmt.Errorf("failed to update backend set %s: %w", backendSetName, err)
	}

	err = m.workRequestsWatcher.WaitFor(ctx, *ociUpdateResp.OpcWorkRequestId)
	if err != nil {
		return fmt.Errorf("failed to wait for backend set %s to be updated: %w", backendSetName, err)
	}
	return nil
}

func makeUpdateOciBackendSetDetails(
	existingBackendSet loadbalancer.BackendSet,
	newBackends []loadbalancer.BackendDetails,
) loadbalancer.UpdateBackendSetDetails {
	updateDetails := loadbalancer.UpdateBackendSetDetails{
		Backends: newBackends,

		Policy:                                  existingBackendSet.Policy,
		SessionPersistenceConfiguration:         existingBackendSet.SessionPersistenceConfiguration,
		LbCookieSessionPersistenceConfiguration: existingBackendSet.LbCookieSessionPersistenceConfiguration,
	}

	if existingBackendSet.HealthChecker != nil {
		updateDetails.HealthChecker = &loadbalancer.HealthCheckerDetails{
			Protocol:          existingBackendSet.HealthChecker.Protocol,
			UrlPath:           existingBackendSet.HealthChecker.UrlPath,
			Port:              existingBackendSet.HealthChecker.Port,
			ReturnCode:        existingBackendSet.HealthChecker.ReturnCode,
			Retries:           existingBackendSet.HealthChecker.Retries,
			TimeoutInMillis:   existingBackendSet.HealthChecker.TimeoutInMillis,
			IntervalInMillis:  existingBackendSet.HealthChecker.IntervalInMillis,
			ResponseBodyRegex: existingBackendSet.HealthChecker.ResponseBodyRegex,
			IsForcePlainText:  existingBackendSet.HealthChecker.IsForcePlainText,
		}
	}

	if existingBackendSet.SslConfiguration != nil {
		updateDetails.SslConfiguration = &loadbalancer.SslConfigurationDetails{
			CertificateName:                existingBackendSet.SslConfiguration.CertificateName,
			TrustedCertificateAuthorityIds: existingBackendSet.SslConfiguration.TrustedCertificateAuthorityIds,
			VerifyDepth:                    existingBackendSet.SslConfiguration.VerifyDepth,
			VerifyPeerCertificate:          existingBackendSet.SslConfiguration.VerifyPeerCertificate,
			CertificateIds:                 existingBackendSet.SslConfiguration.CertificateIds,
			CipherSuiteName:                existingBackendSet.SslConfiguration.CipherSuiteName,
			ServerOrderPreference: loadbalancer.SslConfigurationDetailsServerOrderPreferenceEnum(
				existingBackendSet.SslConfiguration.ServerOrderPreference,
			),
			Protocols:            existingBackendSet.SslConfiguration.Protocols,
			HasSessionResumption: existingBackendSet.SslConfiguration.HasSessionResumption,
		}
	}

	return updateDetails
}

// httpBackendModelDeps contains the dependencies for the HTTPBackendModel.
type httpBackendModelDeps struct {
	dig.In `ignore-unexported:"true"`

	RootLogger            *slog.Logger
	K8sClient             k8sClient
	OciLoadBalancerClient ociLoadBalancerClient
	WorkRequestsWatcher   workRequestsWatcher

	// Used to allow mocking own methods in tests
	self httpBackendModel
}

// newHTTPBackendModel creates a new HTTPBackendModel.
func newHTTPBackendModel(deps httpBackendModelDeps) httpBackendModel {
	model := &httpBackendModelImpl{
		logger:              deps.RootLogger.WithGroup("http-backend-model"),
		k8sClient:           deps.K8sClient,
		ociClient:           deps.OciLoadBalancerClient,
		workRequestsWatcher: deps.WorkRequestsWatcher,
		self:                deps.self,
	}
	model.self = lo.Ternary[httpBackendModel](model.self != nil, model.self, model)
	return model
}
