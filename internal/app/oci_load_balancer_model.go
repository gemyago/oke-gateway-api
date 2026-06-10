package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	corev1 "k8s.io/api/core/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
)

const defaultBackendSetPort = 80
const defaultCatchAllRuleName = "default_catch_all"
const maxBackendSetNameLength = 32
const maxListenerPolicyNameLength = 32
const listenerPolicyNameHashLength = 16
const ociListenerProtocolHTTP = "HTTP"
const ociListenerProtocolHTTP2 = "HTTP2"

type reconcileDefaultBackendParams struct {
	loadBalancerID   string
	knownBackendSets map[string]loadbalancer.BackendSet
	gateway          *gatewayv1.Gateway
}

type reconcileBackendSetParams struct {
	loadBalancerID string
	service        corev1.Service
	routeNS        string
	backendRef     gatewayv1.BackendRef
}

type deprovisionBackendSetParams struct {
	loadBalancerID string
	httpRoute      gatewayv1.HTTPRoute
	backendRef     gatewayv1.HTTPBackendRef
}

type reconcileHTTPListenerParams struct {
	loadBalancerID        string
	knownListeners        map[string]loadbalancer.Listener
	knownRoutingPolicies  map[string]loadbalancer.RoutingPolicy
	listenerCertificates  []loadbalancer.Certificate
	listenerCertificateID string
	defaultBackendSetName string
	listenerSpec          *gatewayv1.Listener
}

type reconcileListenersCertificatesParams struct {
	loadBalancerID    string
	gateway           *gatewayv1.Gateway
	knownCertificates map[string]loadbalancer.Certificate
}

type reconcileListenersCertificatesResult struct {
	// List of all certificates by certificate name
	reconciledCertificates map[string]loadbalancer.Certificate

	// List of certificates by listener name
	certificatesByListener map[string][]loadbalancer.Certificate

	// List of OCI Certificates Service certificate IDs by listener name.
	certificateIDsByListener map[string]string
}

type makeRoutingRuleParams struct {
	httpRoute          gatewayv1.HTTPRoute
	httpRouteRuleIndex int
}

type makeGRPCRoutingRuleParams struct {
	grpcRoute          gatewayv1.GRPCRoute
	grpcRouteRuleIndex int
}

type makeBackendRoutingRuleParams[T any] struct {
	ruleName            string
	routeKind           string
	routeName           string
	routeRuleIndex      int
	backendRefs         []T
	backendSetName      func(T) string
	mapCondition        func() (string, error)
	conditionErrContext string
}

type commitRoutingPolicyParams struct {
	loadBalancerID string
	listenerName   string
	policyRules    []loadbalancer.RoutingRule

	// Previously programmed policy rules. This parameter helps to detect
	// rules that are not programmed anymore and needs to be removed. They are no
	// longer in the policyRules so there is no way to detect them otherwise.
	prevPolicyRules []string
}

type removeMissingListenersParams struct {
	loadBalancerID   string
	knownListeners   map[string]loadbalancer.Listener
	gatewayListeners []gatewayv1.Listener
}

type removeUnusedCertificatesParams struct {
	loadBalancerID                   string
	previouslyProgrammedCertificates []string
	desiredCertificates              []string
	knownCertificates                map[string]loadbalancer.Certificate
}

type ociLoadBalancerModel interface {
	reconcileDefaultBackendSet(
		ctx context.Context,
		params reconcileDefaultBackendParams,
	) (loadbalancer.BackendSet, error)

	reconcileListenersCertificates(
		ctx context.Context,
		params reconcileListenersCertificatesParams,
	) (reconcileListenersCertificatesResult, error)

	reconcileHTTPListener(
		ctx context.Context,
		params reconcileHTTPListenerParams,
	) error

	ensureHTTP2ListenerProtocol(
		ctx context.Context,
		params ensureHTTP2ListenerProtocolParams,
	) error

	reconcileBackendSet(
		ctx context.Context,
		params reconcileBackendSetParams,
	) error

	deprovisionBackendSet(
		ctx context.Context,
		params deprovisionBackendSetParams,
	) error

	// makeRoutingRule appends a new routing rule to the routing policy.
	makeRoutingRule(
		ctx context.Context,
		params makeRoutingRuleParams,
	) (loadbalancer.RoutingRule, error)

	makeGRPCRoutingRule(
		ctx context.Context,
		params makeGRPCRoutingRuleParams,
	) (loadbalancer.RoutingRule, error)

	commitRoutingPolicy(
		ctx context.Context,
		params commitRoutingPolicyParams,
	) error

	// removeMissingListeners removes listeners from the load balancer that are not present in the gateway spec.
	removeMissingListeners(ctx context.Context, params removeMissingListenersParams) error

	removeUnusedCertificates(
		ctx context.Context,
		params removeUnusedCertificatesParams,
	) error
}

type ociLoadBalancerModelImpl struct {
	k8sClient           k8sClient
	ociClient           ociLoadBalancerClient
	logger              *slog.Logger
	workRequestsWatcher workRequestsWatcher
	routingRulesMapper  ociLoadBalancerRoutingRulesMapper
	routingPolicyLocks  sync.Map
}

type ensureHTTP2ListenerProtocolParams struct {
	loadBalancerID string
	listenerName   string
}

func loadBalancerHealthCheckerMatches(
	current *loadbalancer.HealthChecker,
	desired loadbalancer.HealthCheckerDetails,
) bool {
	if current == nil {
		return false
	}
	return lo.FromPtr(current.Protocol) == lo.FromPtr(desired.Protocol) &&
		lo.FromPtr(current.Port) == lo.FromPtr(desired.Port)
}

func loadBalancerBackendSetMatches(
	current loadbalancer.BackendSet,
	policy string,
	healthChecker loadbalancer.HealthCheckerDetails,
) bool {
	return lo.FromPtr(current.Policy) == policy &&
		loadBalancerHealthCheckerMatches(current.HealthChecker, healthChecker)
}

func loadBalancerBackendSetHealthChecker(port int) loadbalancer.HealthCheckerDetails {
	return loadbalancer.HealthCheckerDetails{
		Protocol: new("TCP"),
		Port:     new(port),
	}
}

func (m *ociLoadBalancerModelImpl) updateBackendSetConfig(
	ctx context.Context,
	loadBalancerID string,
	backendSetName string,
	backendSet loadbalancer.BackendSet,
	policy string,
	healthChecker loadbalancer.HealthCheckerDetails,
) error {
	updateRes, err := m.ociClient.UpdateBackendSet(ctx, loadbalancer.UpdateBackendSetRequest{
		LoadBalancerId: new(loadBalancerID),
		BackendSetName: new(backendSetName),
		UpdateBackendSetDetails: loadbalancer.UpdateBackendSetDetails{
			Backends:                                lo.Map(backendSet.Backends, ociBackendToDetails),
			Policy:                                  new(policy),
			HealthChecker:                           &healthChecker,
			SessionPersistenceConfiguration:         backendSet.SessionPersistenceConfiguration,
			LbCookieSessionPersistenceConfiguration: backendSet.LbCookieSessionPersistenceConfiguration,
			SslConfiguration:                        sslConfigurationDetailsFromBackendSet(backendSet.SslConfiguration),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to update backend set %s: %w", backendSetName, err)
	}
	if updateRes.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to update backend set %s: missing work request id", backendSetName)
	}
	if err = m.workRequestsWatcher.WaitFor(ctx, *updateRes.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed to wait for backend set %s update: %w", backendSetName, err)
	}
	return nil
}

func sslConfigurationDetailsFromBackendSet(
	config *loadbalancer.SslConfiguration,
) *loadbalancer.SslConfigurationDetails {
	if config == nil {
		return nil
	}
	return &loadbalancer.SslConfigurationDetails{
		VerifyDepth:                    config.VerifyDepth,
		VerifyPeerCertificate:          config.VerifyPeerCertificate,
		HasSessionResumption:           config.HasSessionResumption,
		TrustedCertificateAuthorityIds: config.TrustedCertificateAuthorityIds,
		CertificateIds:                 config.CertificateIds,
		CertificateName:                config.CertificateName,
		Protocols:                      config.Protocols,
		CipherSuiteName:                config.CipherSuiteName,
		ServerOrderPreference: loadbalancer.SslConfigurationDetailsServerOrderPreferenceEnum(
			config.ServerOrderPreference,
		),
	}
}

func ociBackendToDetails(backend loadbalancer.Backend, _ int) loadbalancer.BackendDetails {
	return loadbalancer.BackendDetails{
		Backup:         backend.Backup,
		Drain:          backend.Drain,
		IpAddress:      backend.IpAddress,
		Offline:        backend.Offline,
		Port:           backend.Port,
		Weight:         backend.Weight,
		MaxConnections: backend.MaxConnections,
	}
}

func healthCheckerFromDetails(details loadbalancer.HealthCheckerDetails) *loadbalancer.HealthChecker {
	return &loadbalancer.HealthChecker{
		Protocol:          details.Protocol,
		Port:              details.Port,
		ReturnCode:        details.ReturnCode,
		ResponseBodyRegex: details.ResponseBodyRegex,
		UrlPath:           details.UrlPath,
		Retries:           details.Retries,
		TimeoutInMillis:   details.TimeoutInMillis,
		IntervalInMillis:  details.IntervalInMillis,
		IsForcePlainText:  details.IsForcePlainText,
	}
}

func (m *ociLoadBalancerModelImpl) reconcileDefaultBackendSet(
	ctx context.Context,
	params reconcileDefaultBackendParams,
) (loadbalancer.BackendSet, error) {
	defaultBackendSetName := params.gateway.Name + "-default"
	desiredPolicy := "ROUND_ROBIN"
	desiredHealthChecker := loadBalancerBackendSetHealthChecker(defaultBackendSetPort)
	if existingBackendSet, ok := params.knownBackendSets[defaultBackendSetName]; ok {
		if !loadBalancerBackendSetMatches(existingBackendSet, desiredPolicy, desiredHealthChecker) {
			if err := m.updateBackendSetConfig(
				ctx,
				params.loadBalancerID,
				defaultBackendSetName,
				existingBackendSet,
				desiredPolicy,
				desiredHealthChecker,
			); err != nil {
				return loadbalancer.BackendSet{}, err
			}
			existingBackendSet.Policy = new(desiredPolicy)
			existingBackendSet.HealthChecker = healthCheckerFromDetails(desiredHealthChecker)
		}
		m.logger.DebugContext(ctx, "Default backend set already exists",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("backendName", defaultBackendSetName),
		)
		return existingBackendSet, nil
	}

	m.logger.InfoContext(ctx, "Default backend set not found, creating",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("name", defaultBackendSetName),
	)
	createRes, err := m.ociClient.CreateBackendSet(ctx, loadbalancer.CreateBackendSetRequest{
		LoadBalancerId: &params.loadBalancerID,
		CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
			Name:          &defaultBackendSetName,
			Policy:        new(desiredPolicy),
			HealthChecker: &desiredHealthChecker,
		},
	})
	if err != nil {
		return loadbalancer.BackendSet{},
			fmt.Errorf("failed to create default backend set %s: %w", defaultBackendSetName, err)
	}
	if createRes.OpcWorkRequestId == nil {
		return loadbalancer.BackendSet{},
			fmt.Errorf("failed to create default backend set %s: missing work request id", defaultBackendSetName)
	}

	if err = m.workRequestsWatcher.WaitFor(
		ctx,
		*createRes.OpcWorkRequestId,
	); err != nil {
		return loadbalancer.BackendSet{},
			fmt.Errorf("failed to wait for default backend set %s: %w", defaultBackendSetName, err)
	}

	res, err := m.ociClient.GetBackendSet(ctx, loadbalancer.GetBackendSetRequest{
		BackendSetName: &defaultBackendSetName,
		LoadBalancerId: new(params.loadBalancerID),
	})
	if err != nil {
		return loadbalancer.BackendSet{}, fmt.Errorf(
			"failed to get default backend set %s: %w",
			defaultBackendSetName,
			err,
		)
	}

	return res.BackendSet, nil
}

func (m *ociLoadBalancerModelImpl) reconcileListenersCertificates(
	ctx context.Context,
	params reconcileListenersCertificatesParams,
) (reconcileListenersCertificatesResult, error) {
	m.logger.DebugContext(ctx, "Reconciling certificates",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.Int("provisionedCertificatesCount", len(params.knownCertificates)),
	)

	resultingCertificates := maps.Clone(params.knownCertificates)
	listenerCertificates := make(map[string][]loadbalancer.Certificate)
	certificateIDsByListener := gatewayCertificateIDsByListener(*params.gateway)

	for _, listenerSpec := range params.gateway.Spec.Listeners {
		if _, usesOCICertificate := certificateIDsByListener[string(listenerSpec.Name)]; usesOCICertificate {
			continue
		}
		certs, reconcileErr := m.reconcileListenerSecretCertificates(ctx, listenerSecretCertificatesParams{
			loadBalancerID:        params.loadBalancerID,
			gatewayNamespace:      params.gateway.Namespace,
			listenerSpec:          listenerSpec,
			resultingCertificates: resultingCertificates,
		})
		if reconcileErr != nil {
			return reconcileListenersCertificatesResult{}, reconcileErr
		}
		if len(certs) > 0 {
			listenerCertificates[string(listenerSpec.Name)] = certs
		}
	}

	return reconcileListenersCertificatesResult{
		reconciledCertificates:   resultingCertificates,
		certificatesByListener:   listenerCertificates,
		certificateIDsByListener: certificateIDsByListener,
	}, nil
}

type listenerSecretCertificatesParams struct {
	loadBalancerID        string
	gatewayNamespace      string
	listenerSpec          gatewayv1.Listener
	resultingCertificates map[string]loadbalancer.Certificate
}

func (m *ociLoadBalancerModelImpl) reconcileListenerSecretCertificates(
	ctx context.Context,
	params listenerSecretCertificatesParams,
) ([]loadbalancer.Certificate, error) {
	if params.listenerSpec.TLS == nil {
		return nil, nil
	}

	certificates := make([]loadbalancer.Certificate, 0, len(params.listenerSpec.TLS.CertificateRefs))
	seenRefs := make(map[apitypes.NamespacedName]struct{}, len(params.listenerSpec.TLS.CertificateRefs))
	for _, ref := range params.listenerSpec.TLS.CertificateRefs {
		refName := certificateRefNamespacedName(params.gatewayNamespace, ref)
		if _, exists := seenRefs[refName]; exists {
			continue
		}
		seenRefs[refName] = struct{}{}

		cert, err := m.reconcileListenerSecretCertificate(ctx, params, ref)
		if err != nil {
			return nil, err
		}
		certificates = append(certificates, cert)
	}

	return certificates, nil
}

func (m *ociLoadBalancerModelImpl) reconcileListenerSecretCertificate(
	ctx context.Context,
	params listenerSecretCertificatesParams,
	ref gatewayv1.SecretObjectReference,
) (loadbalancer.Certificate, error) {
	secret, err := m.getListenerCertificateSecret(ctx, params.gatewayNamespace, ref)
	if err != nil {
		return loadbalancer.Certificate{}, err
	}

	certName := ociCertificateNameFromSecret(secret)
	if cert, ok := params.resultingCertificates[certName]; ok {
		m.logCertificateAlreadyExists(ctx, params.loadBalancerID, params.listenerSpec.Name, certName, secret)
		return cert, nil
	}

	cert, err := m.createListenerCertificate(ctx, params.loadBalancerID, params.listenerSpec.Name, certName, secret)
	if err != nil {
		return loadbalancer.Certificate{}, err
	}
	params.resultingCertificates[certName] = cert
	return cert, nil
}

func (m *ociLoadBalancerModelImpl) getListenerCertificateSecret(
	ctx context.Context,
	gatewayNamespace string,
	ref gatewayv1.SecretObjectReference,
) (corev1.Secret, error) {
	secret := corev1.Secret{}
	err := m.k8sClient.Get(ctx, certificateRefNamespacedName(gatewayNamespace, ref), &secret)
	if err != nil {
		return corev1.Secret{}, fmt.Errorf("failed to get secret %s: %w", ref.Name, err)
	}
	return secret, nil
}

func certificateRefNamespacedName(
	gatewayNamespace string,
	ref gatewayv1.SecretObjectReference,
) apitypes.NamespacedName {
	return apitypes.NamespacedName{
		Name:      string(ref.Name),
		Namespace: lo.Ternary(ref.Namespace != nil, string(lo.FromPtr(ref.Namespace)), gatewayNamespace),
	}
}

func (m *ociLoadBalancerModelImpl) createListenerCertificate(
	ctx context.Context,
	loadBalancerID string,
	listenerName gatewayv1.SectionName,
	certName string,
	secret corev1.Secret,
) (loadbalancer.Certificate, error) {
	m.logger.InfoContext(ctx, "Creating certificate",
		slog.String("loadBalancerId", loadBalancerID),
		slog.String("listenerName", string(listenerName)),
		slog.String("certificateName", certName),
		slog.String("secretName", secret.Name),
		slog.String("secretNamespace", secret.Namespace),
		slog.String("secretVersion", secret.ResourceVersion),
	)

	certCreateDetails := loadbalancer.CreateCertificateDetails{
		CertificateName:   &certName,
		PublicCertificate: new(string(secret.Data[corev1.TLSCertKey])),
		PrivateKey:        new(string(secret.Data[corev1.TLSPrivateKeyKey])),
	}
	createRes, createErr := m.ociClient.CreateCertificate(ctx, loadbalancer.CreateCertificateRequest{
		LoadBalancerId:           &loadBalancerID,
		CreateCertificateDetails: certCreateDetails,
	})
	if createErr != nil {
		return loadbalancer.Certificate{}, fmt.Errorf("failed to create certificate %s: %w", certName, createErr)
	}
	if createRes.OpcWorkRequestId == nil {
		return loadbalancer.Certificate{}, fmt.Errorf(
			"failed to create certificate %s: missing work request id",
			certName,
		)
	}

	if err := m.workRequestsWatcher.WaitFor(ctx, *createRes.OpcWorkRequestId); err != nil {
		return loadbalancer.Certificate{}, fmt.Errorf("failed to wait for certificate %s: %w", certName, err)
	}

	return loadbalancer.Certificate{
		CertificateName:   &certName,
		PublicCertificate: certCreateDetails.PublicCertificate,
	}, nil
}

func (m *ociLoadBalancerModelImpl) logCertificateAlreadyExists(
	ctx context.Context,
	loadBalancerID string,
	listenerName gatewayv1.SectionName,
	certName string,
	secret corev1.Secret,
) {
	m.logger.DebugContext(ctx, "Certificate already exists, skipping",
		slog.String("loadBalancerId", loadBalancerID),
		slog.String("listenerName", string(listenerName)),
		slog.String("certificateName", certName),
		slog.String("secretName", secret.Name),
		slog.String("secretNamespace", secret.Namespace),
		slog.String("secretVersion", secret.ResourceVersion),
	)
}

func defaultCatchAllRoutingRule(defaultBackendSetName string) loadbalancer.RoutingRule {
	return loadbalancer.RoutingRule{
		Name:      new(defaultCatchAllRuleName),
		Condition: new("any(http.request.url.path sw '/')"),
		Actions: []loadbalancer.Action{
			loadbalancer.ForwardToBackendSet{
				BackendSetName: new(defaultBackendSetName),
			},
		},
	}
}

func routingRuleForwardsToBackendSet(rule loadbalancer.RoutingRule, backendSetName string) bool {
	if len(rule.Actions) != 1 {
		return false
	}
	forward, ok := rule.Actions[0].(loadbalancer.ForwardToBackendSet)
	return ok && lo.FromPtr(forward.BackendSetName) == backendSetName
}

func defaultCatchAllRuleMatches(rule loadbalancer.RoutingRule, defaultBackendSetName string) bool {
	return lo.FromPtr(rule.Name) == defaultCatchAllRuleName &&
		lo.FromPtr(rule.Condition) == "any(http.request.url.path sw '/')" &&
		routingRuleForwardsToBackendSet(rule, defaultBackendSetName)
}

func routingPolicyDefaultRuleDrifted(policy loadbalancer.RoutingPolicy, defaultBackendSetName string) bool {
	for _, rule := range policy.Rules {
		if lo.FromPtr(rule.Name) == defaultCatchAllRuleName {
			return !defaultCatchAllRuleMatches(rule, defaultBackendSetName)
		}
	}
	return true
}

func desiredRoutingPolicyRulesWithDefault(
	policy loadbalancer.RoutingPolicy,
	defaultBackendSetName string,
) []loadbalancer.RoutingRule {
	rules := make([]loadbalancer.RoutingRule, 0, len(policy.Rules)+1)
	defaultRuleFound := false
	for _, rule := range policy.Rules {
		if lo.FromPtr(rule.Name) == defaultCatchAllRuleName {
			defaultRuleFound = true
			rules = append(rules, defaultCatchAllRoutingRule(defaultBackendSetName))
			continue
		}
		rules = append(rules, rule)
	}
	if !defaultRuleFound {
		rules = append(rules, defaultCatchAllRoutingRule(defaultBackendSetName))
	}
	return rules
}

func (m *ociLoadBalancerModelImpl) reconcileListenerRoutingPolicy(
	ctx context.Context,
	params reconcileHTTPListenerParams,
) error {
	listenerName := string(params.listenerSpec.Name)
	routingPolicyName := listenerPolicyName(listenerName)
	policy, ok := params.knownRoutingPolicies[routingPolicyName]

	if !ok {
		return m.createListenerRoutingPolicy(ctx, params, routingPolicyName, listenerName)
	}
	if routingPolicyDefaultRuleDrifted(policy, params.defaultBackendSetName) {
		return m.updateListenerRoutingPolicyDefaultRule(ctx, params, routingPolicyName, policy)
	}

	m.logger.DebugContext(ctx, "Routing policy already exists, skipping creation",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("routingPolicyName", routingPolicyName),
		slog.String("listenerName", listenerName),
	)

	return nil
}

func (m *ociLoadBalancerModelImpl) createListenerRoutingPolicy(
	ctx context.Context,
	params reconcileHTTPListenerParams,
	routingPolicyName string,
	listenerName string,
) error {
	m.logger.InfoContext(ctx, "Creating routing policy for listener",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("routingPolicyName", routingPolicyName),
		slog.String("listenerName", listenerName),
	)

	createRoutingPolicyRes, err := m.ociClient.CreateRoutingPolicy(ctx, loadbalancer.CreateRoutingPolicyRequest{
		LoadBalancerId: &params.loadBalancerID,
		CreateRoutingPolicyDetails: loadbalancer.CreateRoutingPolicyDetails{
			Name:                     new(routingPolicyName),
			ConditionLanguageVersion: loadbalancer.CreateRoutingPolicyDetailsConditionLanguageVersionV1,
			// We're creating routing policy to have it available when reconciling routes.
			// It's not possible to create an empty routing policy, so we're adding a default rule.
			Rules: []loadbalancer.RoutingRule{defaultCatchAllRoutingRule(params.defaultBackendSetName)},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create routing policy %s: %w", routingPolicyName, err)
	}
	if createRoutingPolicyRes.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to create routing policy %s: missing work request id", routingPolicyName)
	}

	if err = m.workRequestsWatcher.WaitFor(ctx, *createRoutingPolicyRes.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed to wait for routing policy %s: %w", routingPolicyName, err)
	}

	return nil
}

func (m *ociLoadBalancerModelImpl) updateListenerRoutingPolicyDefaultRule(
	ctx context.Context,
	params reconcileHTTPListenerParams,
	routingPolicyName string,
	policy loadbalancer.RoutingPolicy,
) error {
	updateRoutingPolicyRes, err := m.ociClient.UpdateRoutingPolicy(ctx, loadbalancer.UpdateRoutingPolicyRequest{
		LoadBalancerId:    &params.loadBalancerID,
		RoutingPolicyName: &routingPolicyName,
		UpdateRoutingPolicyDetails: loadbalancer.UpdateRoutingPolicyDetails{
			ConditionLanguageVersion: loadbalancer.UpdateRoutingPolicyDetailsConditionLanguageVersionEnum(
				policy.ConditionLanguageVersion,
			),
			Rules: desiredRoutingPolicyRulesWithDefault(policy, params.defaultBackendSetName),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to update routing policy %s: %w", routingPolicyName, err)
	}
	if updateRoutingPolicyRes.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to update routing policy %s: missing work request id", routingPolicyName)
	}
	if err = m.workRequestsWatcher.WaitFor(ctx, *updateRoutingPolicyRes.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed to wait for routing policy %s update: %w", routingPolicyName, err)
	}

	return nil
}

func (m *ociLoadBalancerModelImpl) reconcileExistingBackendSet(
	ctx context.Context,
	params reconcileBackendSetParams,
	backendSetName string,
	existingBackendSet loadbalancer.BackendSet,
	desiredPolicy string,
	desiredHealthChecker loadbalancer.HealthCheckerDetails,
) error {
	if !loadBalancerBackendSetMatches(existingBackendSet, desiredPolicy, desiredHealthChecker) {
		if err := m.updateBackendSetConfig(
			ctx,
			params.loadBalancerID,
			backendSetName,
			existingBackendSet,
			desiredPolicy,
			desiredHealthChecker,
		); err != nil {
			return err
		}
	}

	m.logger.DebugContext(ctx, "Backend set found",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("backendSetName", backendSetName),
	)

	return nil
}

func backendSetLookupFound(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	serviceErr, ok := common.IsServiceError(err)
	if ok && serviceErr.GetHTTPStatusCode() == http.StatusNotFound {
		return false, nil
	}

	return false, err
}

func (m *ociLoadBalancerModelImpl) reconcileHTTPListener(
	ctx context.Context,
	params reconcileHTTPListenerParams,
) error {
	listenerName := string(params.listenerSpec.Name)

	if err := m.reconcileListenerRoutingPolicy(ctx, params); err != nil {
		return fmt.Errorf("failed to reconcile listener routing policy: %w", err)
	}

	var sslConfig *loadbalancer.SslConfigurationDetails
	if params.listenerCertificateID != "" {
		sslConfig = &loadbalancer.SslConfigurationDetails{
			CertificateIds: []string{params.listenerCertificateID},
		}
	} else if params.listenerSpec.TLS != nil {
		if len(params.listenerCertificates) == 0 {
			return &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionAccepted),
				reason:        string(gatewayv1.GatewayReasonInvalidParameters),
				message: fmt.Sprintf(
					"listener %s requires certificateRefs or %s TLS option",
					listenerName,
					ListenerTLSOptionOCICertificateOCID,
				),
			}
		}
		cert := params.listenerCertificates[0]

		sslConfig = &loadbalancer.SslConfigurationDetails{
			CertificateName: cert.CertificateName,
		}
	}

	if existingListener, ok := params.knownListeners[listenerName]; ok {
		return m.reconcileExistingHTTPListener(ctx, params, listenerName, existingListener, sslConfig)
	}

	return m.createHTTPListener(ctx, params, listenerName, sslConfig)
}

func (m *ociLoadBalancerModelImpl) reconcileExistingHTTPListener(
	ctx context.Context,
	params reconcileHTTPListenerParams,
	listenerName string,
	existingListener loadbalancer.Listener,
	sslConfig *loadbalancer.SslConfigurationDetails,
) error {
	updateDetails, hasChanges := makeOciListenerUpdateDetails(makeOciListenerUpdateDetailsParams{
		existingListenerData:  existingListener,
		listenerName:          listenerName,
		listenerSpec:          params.listenerSpec,
		defaultBackendSetName: params.defaultBackendSetName,
		sslConfig:             sslConfig,
	})
	if !hasChanges {
		m.logger.DebugContext(ctx, "Listener already up to date, skipping update",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("listenerName", listenerName),
		)
		return nil
	}

	m.logger.DebugContext(ctx, "Updating existing listener",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("listenerName", listenerName),
	)

	updateRes, err := m.ociClient.UpdateListener(ctx, loadbalancer.UpdateListenerRequest{
		ListenerName:          &listenerName,
		LoadBalancerId:        &params.loadBalancerID,
		UpdateListenerDetails: updateDetails,
	})
	if err != nil {
		return fmt.Errorf("failed to update listener %s: %w", listenerName, err)
	}
	if updateRes.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to update listener %s: missing work request id", listenerName)
	}
	if err = m.workRequestsWatcher.WaitFor(ctx, *updateRes.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed to wait for listener %s: %w", listenerName, err)
	}

	return nil
}

func (m *ociLoadBalancerModelImpl) createHTTPListener(
	ctx context.Context,
	params reconcileHTTPListenerParams,
	listenerName string,
	sslConfig *loadbalancer.SslConfigurationDetails,
) error {
	m.logger.InfoContext(ctx, "Listener not found, creating",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("name", listenerName),
	)

	createRes, err := m.ociClient.CreateListener(ctx, loadbalancer.CreateListenerRequest{
		LoadBalancerId: &params.loadBalancerID,
		CreateListenerDetails: loadbalancer.CreateListenerDetails{
			Name:                  new(listenerName),
			DefaultBackendSetName: new(params.defaultBackendSetName),
			Port:                  new(int(params.listenerSpec.Port)),
			Protocol:              new(ociListenerProtocolHTTP),
			RoutingPolicyName:     new(listenerPolicyName(listenerName)),
			SslConfiguration:      sslConfig,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create listener %s: %w", listenerName, err)
	}
	if createRes.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to create listener %s: missing work request id", listenerName)
	}
	if err = m.workRequestsWatcher.WaitFor(ctx, *createRes.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed to wait for listener %s: %w", listenerName, err)
	}

	return nil
}

func (m *ociLoadBalancerModelImpl) ensureHTTP2ListenerProtocol(
	ctx context.Context,
	params ensureHTTP2ListenerProtocolParams,
) error {
	getRes, err := m.ociClient.GetLoadBalancer(ctx, loadbalancer.GetLoadBalancerRequest{
		LoadBalancerId: new(params.loadBalancerID),
	})
	if err != nil {
		return fmt.Errorf("failed to get load balancer %s: %w", params.loadBalancerID, err)
	}

	listener, ok := getRes.LoadBalancer.Listeners[params.listenerName]
	if !ok {
		return fmt.Errorf("listener %s not found", params.listenerName)
	}
	if lo.FromPtr(listener.Protocol) == ociListenerProtocolHTTP2 {
		return nil
	}

	updateDetails := loadbalancer.UpdateListenerDetails{
		DefaultBackendSetName:   listener.DefaultBackendSetName,
		Port:                    listener.Port,
		Protocol:                new(ociListenerProtocolHTTP2),
		HostnameNames:           listener.HostnameNames,
		PathRouteSetName:        listener.PathRouteSetName,
		RoutingPolicyName:       listener.RoutingPolicyName,
		SslConfiguration:        sslConfigurationDetailsFromBackendSet(listener.SslConfiguration),
		ConnectionConfiguration: listener.ConnectionConfiguration,
		RuleSetNames:            listener.RuleSetNames,
	}

	updateRes, err := m.ociClient.UpdateListener(ctx, loadbalancer.UpdateListenerRequest{
		LoadBalancerId:        new(params.loadBalancerID),
		ListenerName:          new(params.listenerName),
		UpdateListenerDetails: updateDetails,
	})
	if err != nil {
		return fmt.Errorf("failed to update listener %s protocol to HTTP2: %w", params.listenerName, err)
	}
	if updateRes.OpcWorkRequestId == nil {
		return fmt.Errorf(
			"failed to update listener %s protocol to HTTP2: missing work request id",
			params.listenerName,
		)
	}
	if err = m.workRequestsWatcher.WaitFor(ctx, *updateRes.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed to wait for listener %s protocol update: %w", params.listenerName, err)
	}

	return nil
}

func (m *ociLoadBalancerModelImpl) reconcileBackendSet(
	ctx context.Context,
	params reconcileBackendSetParams,
) error {
	backendSetName := ociBackendSetNameFromBackendObjectRef(params.routeNS, params.backendRef.BackendObjectReference)
	healthCheckerPort := int(lo.FromPtr(params.backendRef.BackendObjectReference.Port))
	if healthCheckerPort == 0 && len(params.service.Spec.Ports) > 0 {
		healthCheckerPort = params.service.Spec.Ports[0].TargetPort.IntValue()
	}
	if healthCheckerPort == 0 {
		// Not the best option. Potentially have to be refactored to use
		// port from the backend ref. Some research is needed.
		healthCheckerPort = int(params.service.Spec.Ports[0].Port)
	}
	desiredPolicy := "ROUND_ROBIN"
	desiredHealthChecker := loadBalancerBackendSetHealthChecker(healthCheckerPort)

	getResponse, err := m.ociClient.GetBackendSet(ctx, loadbalancer.GetBackendSetRequest{
		BackendSetName: &backendSetName,
		LoadBalancerId: &params.loadBalancerID,
	})
	found, lookupErr := backendSetLookupFound(err)
	if lookupErr != nil {
		return fmt.Errorf("failed to get backend set %s: %w", backendSetName, lookupErr)
	}
	if found {
		return m.reconcileExistingBackendSet(
			ctx,
			params,
			backendSetName,
			getResponse.BackendSet,
			desiredPolicy,
			desiredHealthChecker,
		)
	}

	m.logger.InfoContext(ctx, "Backend set not found, creating",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("backendSetName", backendSetName),
		slog.Int("healthCheckerPort", healthCheckerPort),
	)

	createRes, err := m.ociClient.CreateBackendSet(ctx, loadbalancer.CreateBackendSetRequest{
		LoadBalancerId: &params.loadBalancerID,
		CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
			Name:          &backendSetName,
			Policy:        new(desiredPolicy),
			HealthChecker: &desiredHealthChecker,
		},
	})

	if err != nil {
		return fmt.Errorf("failed to create backend set %s: %w", backendSetName, err)
	}
	if createRes.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to create backend set %s: missing work request id", backendSetName)
	}

	if err = m.workRequestsWatcher.WaitFor(
		ctx,
		*createRes.OpcWorkRequestId,
	); err != nil {
		return fmt.Errorf("failed to wait for backend set %s: %w", backendSetName, err)
	}

	return nil
}

func (m *ociLoadBalancerModelImpl) deprovisionBackendSet(
	ctx context.Context,
	params deprovisionBackendSetParams,
) error {
	backendSetName := ociBackendSetNameFromBackendRef(params.httpRoute, params.backendRef)

	m.logger.InfoContext(ctx, "Deprovisioning backend set",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("backendSetName", backendSetName),
	)

	deleteRes, err := m.ociClient.DeleteBackendSet(ctx, loadbalancer.DeleteBackendSetRequest{
		LoadBalancerId: &params.loadBalancerID,
		BackendSetName: &backendSetName,
	})
	if err != nil {
		serviceErr, ok := common.IsServiceError(err)
		if ok && serviceErr.GetHTTPStatusCode() == http.StatusNotFound {
			m.logger.InfoContext(ctx, "Backend set not found, assuming already deprovisioned",
				slog.String("loadBalancerId", params.loadBalancerID),
				slog.String("backendSetName", backendSetName),
			)
			return nil // Already gone
		}
		if ok && serviceErr.GetHTTPStatusCode() == http.StatusBadRequest &&
			serviceErr.GetCode() == "InvalidParameter" &&
			strings.Contains(serviceErr.GetMessage(), "used in routing policy") {
			m.logger.InfoContext(ctx, "Backend set is used in routing policy, skipping deletion",
				slog.String("loadBalancerId", params.loadBalancerID),
				slog.String("backendSetName", backendSetName),
				slog.Any("serviceError", err),
			)
			return nil // Skip deletion as it's used in routing policy
		}
		return fmt.Errorf("failed to delete backend set %s: %w", backendSetName, err)
	}
	if deleteRes.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to delete backend set %s: missing work request id", backendSetName)
	}

	if err = m.workRequestsWatcher.WaitFor(
		ctx,
		*deleteRes.OpcWorkRequestId,
	); err != nil {
		return fmt.Errorf("failed to wait for backend set %s deletion: %w", backendSetName, err)
	}

	m.logger.InfoContext(ctx, "Successfully deprovisioned backend set",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("backendSetName", backendSetName),
	)
	return nil
}

func (m *ociLoadBalancerModelImpl) makeRoutingRule(
	ctx context.Context,
	params makeRoutingRuleParams,
) (loadbalancer.RoutingRule, error) {
	rule := params.httpRoute.Spec.Rules[params.httpRouteRuleIndex]
	return makeBackendRoutingRule(ctx, makeBackendRoutingRuleParams[gatewayv1.HTTPBackendRef]{
		ruleName:       ociListerPolicyRuleName(params.httpRoute, params.httpRouteRuleIndex),
		routeKind:      "httpRoute",
		routeName:      fmt.Sprintf("%s/%s", params.httpRoute.Namespace, params.httpRoute.Name),
		routeRuleIndex: params.httpRouteRuleIndex,
		backendRefs:    rule.BackendRefs,
		backendSetName: func(backendRef gatewayv1.HTTPBackendRef) string {
			return ociBackendSetNameFromBackendRef(params.httpRoute, backendRef)
		},
		mapCondition: func() (string, error) {
			return m.routingRulesMapper.mapHTTPRouteHostnamesAndMatchesToCondition(
				params.httpRoute.Spec.Hostnames,
				rule.Matches,
			)
		},
		conditionErrContext: "failed to map http route matches to condition",
	}, m.buildForwardRoutingRule)
}

func (m *ociLoadBalancerModelImpl) makeGRPCRoutingRule(
	ctx context.Context,
	params makeGRPCRoutingRuleParams,
) (loadbalancer.RoutingRule, error) {
	rule := params.grpcRoute.Spec.Rules[params.grpcRouteRuleIndex]
	return makeBackendRoutingRule(ctx, makeBackendRoutingRuleParams[gatewayv1.GRPCBackendRef]{
		ruleName:       ociGRPCListenerPolicyRuleName(params.grpcRoute, params.grpcRouteRuleIndex),
		routeKind:      "grpcRoute",
		routeName:      fmt.Sprintf("%s/%s", params.grpcRoute.Namespace, params.grpcRoute.Name),
		routeRuleIndex: params.grpcRouteRuleIndex,
		backendRefs:    rule.BackendRefs,
		backendSetName: func(backendRef gatewayv1.GRPCBackendRef) string {
			return ociBackendSetNameFromGRPCBackendRef(params.grpcRoute, backendRef)
		},
		mapCondition: func() (string, error) {
			return m.routingRulesMapper.mapGRPCRouteHostnamesAndMatchesToCondition(
				params.grpcRoute.Spec.Hostnames,
				rule.Matches,
			)
		},
		conditionErrContext: "failed to map grpc route matches to condition",
	}, m.buildForwardRoutingRule)
}

type buildForwardRoutingRuleParams struct {
	routeKind      string
	routeName      string
	routeRuleIndex int
	ruleName       string
	condition      string
	targetBackends []string
}

func makeBackendRoutingRule[T any](
	ctx context.Context,
	params makeBackendRoutingRuleParams[T],
	buildForwardRoutingRule func(context.Context, buildForwardRoutingRuleParams) loadbalancer.RoutingRule,
) (loadbalancer.RoutingRule, error) {
	targetBackends := lo.Map(params.backendRefs, func(backendRef T, _ int) string {
		return params.backendSetName(backendRef)
	})

	condition, err := params.mapCondition()
	if err != nil {
		return loadbalancer.RoutingRule{}, fmt.Errorf("%s: %w", params.conditionErrContext, err)
	}

	return buildForwardRoutingRule(ctx, buildForwardRoutingRuleParams{
		routeKind:      params.routeKind,
		routeName:      params.routeName,
		routeRuleIndex: params.routeRuleIndex,
		ruleName:       params.ruleName,
		condition:      condition,
		targetBackends: targetBackends,
	}), nil
}

func (m *ociLoadBalancerModelImpl) buildForwardRoutingRule(
	ctx context.Context,
	params buildForwardRoutingRuleParams,
) loadbalancer.RoutingRule {
	m.logger.DebugContext(ctx, "Building OCI routing rule",
		slog.String(params.routeKind, params.routeName),
		slog.Int(params.routeKind+"RuleIndex", params.routeRuleIndex),
		slog.String("ruleName", params.ruleName),
		slog.Any("targetBackends", params.targetBackends),
	)
	return loadbalancer.RoutingRule{
		Name:      new(params.ruleName),
		Condition: new(params.condition),
		Actions: lo.Map(params.targetBackends, func(backendSetName string, _ int) loadbalancer.Action {
			return loadbalancer.ForwardToBackendSet{
				BackendSetName: new(backendSetName),
			}
		}),
	}
}

func (m *ociLoadBalancerModelImpl) deleteMissingListener(
	ctx context.Context,
	loadBalancerID string,
	listener loadbalancer.Listener,
) error {
	m.logger.InfoContext(ctx, "Removing listener not found in gateway spec",
		slog.String("listenerName", lo.FromPtr(listener.Name)),
		slog.String("loadBalancerId", loadBalancerID),
		slog.String("routingPolicyName", lo.FromPtr(listener.RoutingPolicyName)),
	)
	resp, err := m.ociClient.DeleteListener(ctx, loadbalancer.DeleteListenerRequest{
		LoadBalancerId: &loadBalancerID,
		ListenerName:   listener.Name,
	})
	if err != nil {
		m.logger.WarnContext(ctx,
			"Listener deletion failed, will try with others",
			diag.ErrAttr(err),
			slog.String("listenerName", lo.FromPtr(listener.Name)),
			slog.String("loadBalancerId", loadBalancerID),
		)
		return fmt.Errorf("failed to delete listener %s: %w", lo.FromPtr(listener.Name), err)
	}
	if resp.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to delete listener %s: missing work request id", lo.FromPtr(listener.Name))
	}

	if err = m.workRequestsWatcher.WaitFor(ctx, *resp.OpcWorkRequestId); err != nil {
		return fmt.Errorf("failed to wait for listener %s deletion: %w", lo.FromPtr(listener.Name), err)
	}

	return nil
}

func (m *ociLoadBalancerModelImpl) deleteMissingRoutingPolicy(
	ctx context.Context,
	loadBalancerID string,
	listener loadbalancer.Listener,
) error {
	if listener.RoutingPolicyName != nil {
		m.logger.DebugContext(ctx, "Deleting routing policy",
			slog.String("routingPolicyName", *listener.RoutingPolicyName),
			slog.String("loadBalancerId", loadBalancerID),
		)
		var deletePolicyRes loadbalancer.DeleteRoutingPolicyResponse
		deletePolicyRes, err := m.ociClient.DeleteRoutingPolicy(ctx, loadbalancer.DeleteRoutingPolicyRequest{
			LoadBalancerId:    &loadBalancerID,
			RoutingPolicyName: listener.RoutingPolicyName,
		})
		if err != nil {
			return fmt.Errorf("failed to delete routing policy %s: %w", *listener.RoutingPolicyName, err)
		}
		if deletePolicyRes.OpcWorkRequestId == nil {
			return fmt.Errorf(
				"failed to delete routing policy %s: missing work request id",
				*listener.RoutingPolicyName,
			)
		}

		if err = m.workRequestsWatcher.WaitFor(ctx, *deletePolicyRes.OpcWorkRequestId); err != nil {
			return fmt.Errorf("failed to wait for routing policy %s deletion: %w", *listener.RoutingPolicyName, err)
		}
	}

	return nil
}

func (m *ociLoadBalancerModelImpl) removeMissingListeners(
	ctx context.Context,
	params removeMissingListenersParams,
) error {
	// TODO: Investigate desired behavior when attempting to delete listeners
	// that have rules associated with them.

	gatewayListenerNames := lo.SliceToMap(params.gatewayListeners, func(l gatewayv1.Listener) (string, struct{}) {
		return string(l.Name), struct{}{}
	})

	var errs []error
	for listenerName, listener := range params.knownListeners {
		if _, existsInGateway := gatewayListenerNames[listenerName]; !existsInGateway {
			if err := m.deleteMissingListener(ctx, params.loadBalancerID, listener); err != nil {
				m.logger.WarnContext(ctx, "Failed to delete listener, will try with others",
					diag.ErrAttr(err),
					slog.String("listenerName", listenerName),
					slog.String("loadBalancerId", params.loadBalancerID),
				)
				errs = append(errs, err)
				continue
			}

			if err := m.deleteMissingRoutingPolicy(ctx, params.loadBalancerID, listener); err != nil {
				m.logger.WarnContext(ctx, "Failed to delete routing policy, will try with others",
					diag.ErrAttr(err),
					slog.String("listenerName", listenerName),
					slog.String("loadBalancerId", params.loadBalancerID),
				)
				errs = append(errs, err)
				continue
			}

			m.logger.DebugContext(ctx, "Completed listener removal", slog.String("listenerName", listenerName))
		}
	}

	return errors.Join(errs...)
}

func routingRuleLess(ruleI, ruleJ loadbalancer.RoutingRule) bool {
	ruleNameI := lo.FromPtr(ruleI.Name)
	ruleNameJ := lo.FromPtr(ruleJ.Name)
	if ruleNameI == defaultCatchAllRuleName {
		return false
	}
	if ruleNameJ == defaultCatchAllRuleName {
		return true
	}
	grpcRuleI := routingRuleMatchesNativeGRPC(ruleI)
	grpcRuleJ := routingRuleMatchesNativeGRPC(ruleJ)
	if grpcRuleI != grpcRuleJ {
		return grpcRuleI
	}
	return ruleNameI < ruleNameJ
}

func routingRuleMatchesNativeGRPC(rule loadbalancer.RoutingRule) bool {
	condition := lo.FromPtr(rule.Condition)
	return strings.Contains(condition, "eq (i 'application/grpc')") ||
		strings.Contains(condition, "sw (i 'application/grpc+')") ||
		strings.Contains(condition, "sw (i 'application/grpc;')")
}

func sortRoutingRules(rules []loadbalancer.RoutingRule) {
	sort.Slice(rules, func(i, j int) bool {
		return routingRuleLess(rules[i], rules[j])
	})
}

func sortedRoutingRules(rules []loadbalancer.RoutingRule) []loadbalancer.RoutingRule {
	sorted := slices.Clone(rules)
	sortRoutingRules(sorted)
	return sorted
}

func routingRulesEqual(rulesA, rulesB []loadbalancer.RoutingRule) bool {
	return reflect.DeepEqual(sortedRoutingRules(rulesA), sortedRoutingRules(rulesB))
}

func (m *ociLoadBalancerModelImpl) commitRoutingPolicy(
	ctx context.Context,
	params commitRoutingPolicyParams,
) error {
	policyName := listenerPolicyName(params.listenerName)
	policyLock := m.routingPolicyLock(params.loadBalancerID, policyName)
	policyLock.Lock()
	defer policyLock.Unlock()

	return m.commitRoutingPolicyLocked(ctx, params, policyName)
}

func (m *ociLoadBalancerModelImpl) commitRoutingPolicyLocked(
	ctx context.Context,
	params commitRoutingPolicyParams,
	policyName string,
) error {
	policyResponse, err := m.ociClient.GetRoutingPolicy(ctx, loadbalancer.GetRoutingPolicyRequest{
		RoutingPolicyName: &policyName,
		LoadBalancerId:    &params.loadBalancerID,
	})
	if err != nil {
		return fmt.Errorf("failed to get routing policy %s: %w", policyName, err)
	}

	currentRulesByName := lo.SliceToMap(
		policyResponse.RoutingPolicy.Rules,
		func(rule loadbalancer.RoutingRule) (string, loadbalancer.RoutingRule) {
			return lo.FromPtr(rule.Name), rule
		},
	)

	policyRulesNames := make(map[string]struct{})
	for _, newRule := range params.policyRules {
		ruleName := lo.FromPtr(newRule.Name)
		currentRulesByName[ruleName] = newRule
		policyRulesNames[ruleName] = struct{}{}
	}

	for _, prevRuleName := range params.prevPolicyRules {
		if _, ok := policyRulesNames[prevRuleName]; !ok {
			m.logger.InfoContext(ctx, "Deleting previous policy rule",
				slog.String("ruleName", prevRuleName),
				slog.String("loadBalancerId", params.loadBalancerID),
				slog.String("policyName", policyName),
			)
			delete(currentRulesByName, prevRuleName)
		}
	}

	mergedRules := lo.Values(currentRulesByName)
	sortRoutingRules(mergedRules)

	if routingRulesEqual(policyResponse.RoutingPolicy.Rules, mergedRules) {
		m.logger.DebugContext(ctx, "Routing policy already up to date, skipping update",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("policyName", policyName),
		)
		return nil
	}

	updateRes, err := m.ociClient.UpdateRoutingPolicy(ctx, loadbalancer.UpdateRoutingPolicyRequest{
		LoadBalancerId:    &params.loadBalancerID,
		RoutingPolicyName: &policyName,
		UpdateRoutingPolicyDetails: loadbalancer.UpdateRoutingPolicyDetails{
			ConditionLanguageVersion: loadbalancer.UpdateRoutingPolicyDetailsConditionLanguageVersionEnum(
				policyResponse.RoutingPolicy.ConditionLanguageVersion,
			),
			Rules: mergedRules,
		},
	})
	if err != nil {
		m.logger.WarnContext(ctx, "Failed to update routing policy",
			diag.ErrAttr(err),
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("policyName", policyName),
			slog.Any("policyRules", mergedRules),
		)
		return fmt.Errorf("failed to update routing policy %s: %w", policyName, err)
	}
	if updateRes.OpcWorkRequestId == nil {
		return fmt.Errorf("failed to update routing policy %s: missing work request id", policyName)
	}

	if err = m.workRequestsWatcher.WaitFor(
		ctx,
		*updateRes.OpcWorkRequestId,
	); err != nil {
		return fmt.Errorf("failed to wait for routing policy %s update: %w", policyName, err)
	}

	m.logger.InfoContext(ctx, "Successfully committed routing policy changes",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("routingPolicyName", policyName),
	)
	return nil
}

func (m *ociLoadBalancerModelImpl) routingPolicyLock(loadBalancerID, policyName string) *sync.Mutex {
	lock, _ := m.routingPolicyLocks.LoadOrStore(loadBalancerID+"/"+policyName, &sync.Mutex{})
	policyLock, ok := lock.(*sync.Mutex)
	if !ok {
		return &sync.Mutex{}
	}
	return policyLock
}

func (m *ociLoadBalancerModelImpl) removeUnusedCertificates(
	ctx context.Context,
	params removeUnusedCertificatesParams,
) error {
	desiredCertificates := make(map[string]struct{}, len(params.desiredCertificates))
	for _, certName := range params.desiredCertificates {
		desiredCertificates[certName] = struct{}{}
	}

	for _, certName := range params.previouslyProgrammedCertificates {
		if _, isDesired := desiredCertificates[certName]; isDesired {
			continue
		}

		cert, exists := params.knownCertificates[certName]
		if !exists {
			continue
		}

		m.logger.InfoContext(ctx, "Removing unused certificate",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("certificateName", certName),
		)

		resp, err := m.ociClient.DeleteCertificate(ctx, loadbalancer.DeleteCertificateRequest{
			LoadBalancerId:  &params.loadBalancerID,
			CertificateName: cert.CertificateName,
		})
		if err != nil {
			m.logger.WarnContext(ctx, "Failed to delete certificate",
				diag.ErrAttr(err),
				slog.String("certificateName", certName),
				slog.String("loadBalancerId", params.loadBalancerID),
			)
			continue
		}
		if resp.OpcWorkRequestId == nil {
			m.logger.WarnContext(ctx, "Failed to delete certificate: missing work request id",
				slog.String("certificateName", certName),
				slog.String("loadBalancerId", params.loadBalancerID),
			)
			continue
		}

		if err = m.workRequestsWatcher.WaitFor(ctx, *resp.OpcWorkRequestId); err != nil {
			m.logger.WarnContext(ctx, "Failed to wait for certificate deletion",
				diag.ErrAttr(err),
				slog.String("loadBalancerId", params.loadBalancerID),
				slog.String("certificateName", certName),
			)
			continue
		}

		m.logger.DebugContext(ctx, "Successfully removed unused certificate",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("certificateName", certName),
		)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("failed to remove unused certificates: %w", err)
	}

	return nil
}

func certificateNamesFromListenerCertificates(
	listenerCertificates map[string][]loadbalancer.Certificate,
) []string {
	certNames := make([]string, 0, len(listenerCertificates))
	for _, certificates := range listenerCertificates {
		for _, cert := range certificates {
			if cert.CertificateName == nil {
				continue
			}
			certNames = append(certNames, *cert.CertificateName)
		}
	}
	return normalizeProgrammedCertificateNames(certNames)
}

func listenerPolicyName(listenerName string) string {
	rawName := listenerName + "_policy"
	if isValidOCIRoutingPolicyName(rawName) {
		return rawName
	}

	hash := listenerPolicyNameHash(listenerName)
	namePrefix := fmt.Sprintf("p_%s_", hash)
	nameSuffix := invalidCharsForPolicyNamePattern.ReplaceAllString(rawName, "_")
	maxSuffixLength := maxListenerPolicyNameLength - len(namePrefix)
	if len(nameSuffix) > maxSuffixLength {
		nameSuffix = nameSuffix[:maxSuffixLength]
	}

	return namePrefix + nameSuffix
}

func listenerPolicyNameHash(listenerName string) string {
	sum := sha256.Sum256([]byte(listenerName))
	return hex.EncodeToString(sum[:])[:listenerPolicyNameHashLength]
}

/*
Name for the routing policy rule set. A name is required. The name must be unique,
and can't be changed. The name can't begin with a period and can't contain any of
these characters: ; ? # / % \ ] [. The name must start with an lower- or upper- case
letter or an underscore, and the rest of the name can contain numbers, underscores,
and upper- or lowercase letters.
*/
var invalidCharsForPolicyNamePattern = regexp.MustCompile(`[^a-zA-Z0-9_]`)
var validOCIRoutingPolicyNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func isValidOCIRoutingPolicyName(policyName string) bool {
	return len(policyName) <= maxListenerPolicyNameLength &&
		validOCIRoutingPolicyNamePattern.MatchString(policyName)
}

// ociListerPolicyRuleName returns the name of the routing rule for the listener policy.
// It's expected that the rule name is unique within the listener policy for every route.
// Names should also be sortable, so we're using a 4 digit index.
func ociListerPolicyRuleName(route gatewayv1.HTTPRoute, ruleIndex int) string {
	rule := route.Spec.Rules[ruleIndex]
	nameParts := []string{route.Namespace, route.Name}

	if rule.Name != nil {
		nameParts = append(nameParts, string(*rule.Name))
	}

	return ociListenerPolicyRuleNameFromParts(ruleIndex, nameParts...)
}

func ociGRPCListenerPolicyRuleName(route gatewayv1.GRPCRoute, ruleIndex int) string {
	rule := route.Spec.Rules[ruleIndex]
	nameParts := []string{"grpc", route.Namespace, route.Name}

	if rule.Name != nil {
		nameParts = append(nameParts, string(*rule.Name))
	}

	return ociListenerPolicyRuleNameFromParts(ruleIndex, nameParts...)
}

func ociListenerPolicyRuleNameFromParts(ruleIndex int, nameParts ...string) string {
	resultingName := fmt.Sprintf(
		"p%04d_%08x_%s",
		ruleIndex,
		crc32.ChecksumIEEE([]byte(ociListenerPolicyRuleIdentity(ruleIndex, nameParts...))),
		strings.Join(nameParts, "_"),
	)

	return ociapi.ConstructOCIResourceName(resultingName, ociapi.OCIResourceNameConfig{
		MaxLength:           maxListenerPolicyNameLength,
		InvalidCharsPattern: invalidCharsForPolicyNamePattern,
	})
}

func ociListenerPolicyRuleIdentity(ruleIndex int, nameParts ...string) string {
	var result strings.Builder
	result.WriteString(strconv.Itoa(ruleIndex))
	for _, part := range nameParts {
		fmt.Fprintf(&result, ":%d:%s", len(part), part)
	}
	return result.String()
}

// ociBackendSetName returns the name of the backend set for the route.
// It's expected that the backend set name is unique within the load balancer for every route.
// Sorting is not required, but keeping padding for consistency and readability.
func ociBackendSetNameFromBackendRef(httpRoute gatewayv1.HTTPRoute, backendRef gatewayv1.HTTPBackendRef) string {
	return ociBackendSetNameFromBackendObjectRef(httpRoute.Namespace, backendRef.BackendObjectReference)
}

func ociBackendSetNameFromGRPCBackendRef(grpcRoute gatewayv1.GRPCRoute, backendRef gatewayv1.GRPCBackendRef) string {
	return ociBackendSetNameFromBackendObjectRef(grpcRoute.Namespace, backendRef.BackendObjectReference)
}

func ociBackendSetNameFromBackendObjectRef(
	defaultNamespace string,
	backendRef gatewayv1.BackendObjectReference,
) string {
	refName := string(backendRef.Name)
	refNamespace := string(lo.FromPtr(backendRef.Namespace))
	if refNamespace == "" {
		refNamespace = defaultNamespace
	}

	originalName := fmt.Sprintf("%s-%s", refNamespace, refName)
	if backendRef.Port != nil {
		originalName = fmt.Sprintf("%s-%d", originalName, *backendRef.Port)
	}

	return ociapi.ConstructOCIResourceName(originalName, ociapi.OCIResourceNameConfig{
		MaxLength: maxBackendSetNameLength,
	})
}

func ociBackendSetNameFromService(service corev1.Service) string {
	originalName := service.Namespace + "-" + service.Name
	return ociapi.ConstructOCIResourceName(originalName, ociapi.OCIResourceNameConfig{
		MaxLength: maxBackendSetNameLength,
	})
}

type makeOciListenerUpdateDetailsParams struct {
	existingListenerData  loadbalancer.Listener
	listenerName          string
	listenerSpec          *gatewayv1.Listener
	defaultBackendSetName string
	sslConfig             *loadbalancer.SslConfigurationDetails
}

func makeOciListenerUpdateDetails(
	params makeOciListenerUpdateDetailsParams,
) (loadbalancer.UpdateListenerDetails, bool) {
	expectedProtocol := expectedHTTPListenerProtocol(params.existingListenerData)
	hasChanges := params.existingListenerData.Protocol == nil ||
		*params.existingListenerData.Protocol != expectedProtocol

	if params.existingListenerData.Port == nil || *params.existingListenerData.Port != int(params.listenerSpec.Port) {
		hasChanges = true
	}

	if lo.FromPtr(params.existingListenerData.DefaultBackendSetName) != params.defaultBackendSetName {
		hasChanges = true
	}

	expectedPolicyName := listenerPolicyName(params.listenerName)
	if lo.FromPtr(params.existingListenerData.RoutingPolicyName) != expectedPolicyName {
		hasChanges = true
	}

	existingCertName := ""
	if params.existingListenerData.SslConfiguration != nil &&
		params.existingListenerData.SslConfiguration.CertificateName != nil {
		existingCertName = *params.existingListenerData.SslConfiguration.CertificateName
	}
	existingCertIDs := normalizeCertificateIDs(nil)
	if params.existingListenerData.SslConfiguration != nil {
		existingCertIDs = normalizeCertificateIDs(params.existingListenerData.SslConfiguration.CertificateIds)
	}

	newCertName := ""
	if params.sslConfig != nil && params.sslConfig.CertificateName != nil {
		newCertName = *params.sslConfig.CertificateName
	}
	newCertIDs := normalizeCertificateIDs(nil)
	if params.sslConfig != nil {
		newCertIDs = normalizeCertificateIDs(params.sslConfig.CertificateIds)
	}

	if existingCertName != newCertName || !slices.Equal(existingCertIDs, newCertIDs) {
		hasChanges = true
	}

	if !hasChanges {
		return loadbalancer.UpdateListenerDetails{}, false
	}

	return loadbalancer.UpdateListenerDetails{
		Protocol:              new(expectedProtocol),
		Port:                  new(int(params.listenerSpec.Port)),
		DefaultBackendSetName: new(params.defaultBackendSetName),
		RoutingPolicyName:     new(expectedPolicyName),
		SslConfiguration:      params.sslConfig,
	}, true
}

func expectedHTTPListenerProtocol(existingListener loadbalancer.Listener) string {
	if lo.FromPtr(existingListener.Protocol) == ociListenerProtocolHTTP2 {
		return ociListenerProtocolHTTP2
	}
	return ociListenerProtocolHTTP
}

func normalizeCertificateIDs(certificateIDs []string) []string {
	if len(certificateIDs) == 0 {
		return nil
	}
	return certificateIDs
}

type ociLoadBalancerModelDeps struct {
	dig.In

	RootLogger          *slog.Logger
	K8sClient           k8sClient
	OciClient           ociLoadBalancerClient
	WorkRequestsWatcher workRequestsWatcher
	RoutingRulesMapper  ociLoadBalancerRoutingRulesMapper
}

func newOciLoadBalancerModel(deps ociLoadBalancerModelDeps) *ociLoadBalancerModelImpl {
	return &ociLoadBalancerModelImpl{
		logger:              deps.RootLogger.WithGroup("oci-load-balancer-model"),
		ociClient:           deps.OciClient,
		k8sClient:           deps.K8sClient,
		workRequestsWatcher: deps.WorkRequestsWatcher,
		routingRulesMapper:  deps.RoutingRulesMapper,
	}
}

func ociCertificateNameFromSecret(
	secret corev1.Secret,
) string {
	return secret.Namespace + "-" + secret.Name + "-rev-" + secret.ResourceVersion
}
