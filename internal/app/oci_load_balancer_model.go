package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services/ociapi"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	v1 "k8s.io/api/core/v1"
	types "k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const defaultBackendSetPort = 80
const defaultCatchAllRuleName = "default_catch_all"
const maxBackendSetNameLength = 32
const maxListenerPolicyNameLength = 32

type reconcileDefaultBackendParams struct {
	loadBalancerID   string
	knownBackendSets map[string]loadbalancer.BackendSet
	gateway          *gatewayv1.Gateway
}

type reconcileBackendSetParams struct {
	loadBalancerID string
	service        v1.Service
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
	knownCertificates     map[string]loadbalancer.Certificate
	defaultBackendSetName string
	listenerSpec          *gatewayv1.Listener
}

type reconcileListenersCertificatesParams struct {
	loadBalancerID    string
	gateway           *gatewayv1.Gateway
	knownCertificates map[string]loadbalancer.Certificate
}

type reconcileListenersCertificatesResult struct {
	knownCertificates map[string]loadbalancer.Certificate
}

type makeRoutingRuleParams struct {
	httpRoute          gatewayv1.HTTPRoute
	httpRouteRuleIndex int
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

	commitRoutingPolicy(
		ctx context.Context,
		params commitRoutingPolicyParams,
	) error

	// removeMissingListeners removes listeners from the load balancer that are not present in the gateway spec.
	removeMissingListeners(ctx context.Context, params removeMissingListenersParams) error
}

type ociLoadBalancerModelImpl struct {
	k8sClient           k8sClient
	ociClient           ociLoadBalancerClient
	logger              *slog.Logger
	workRequestsWatcher workRequestsWatcher
	routingRulesMapper  ociLoadBalancerRoutingRulesMapper
}

func (m *ociLoadBalancerModelImpl) reconcileDefaultBackendSet(
	ctx context.Context,
	params reconcileDefaultBackendParams,
) (loadbalancer.BackendSet, error) {
	defaultBackendSetName := params.gateway.Name + "-default"
	if _, ok := params.knownBackendSets[defaultBackendSetName]; ok {
		m.logger.DebugContext(ctx, "Default backend set already exists",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("backendName", defaultBackendSetName),
		)
		return params.knownBackendSets[defaultBackendSetName], nil
	}

	m.logger.InfoContext(ctx, "Default backend set not found, creating",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("name", defaultBackendSetName),
	)
	createRes, err := m.ociClient.CreateBackendSet(ctx, loadbalancer.CreateBackendSetRequest{
		LoadBalancerId: &params.loadBalancerID,
		CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
			Name:   &defaultBackendSetName,
			Policy: lo.ToPtr("ROUND_ROBIN"),
			HealthChecker: &loadbalancer.HealthCheckerDetails{
				Protocol: lo.ToPtr("TCP"),
				Port:     lo.ToPtr(int(defaultBackendSetPort)),
			},
		},
	})
	if err != nil {
		return loadbalancer.BackendSet{},
			fmt.Errorf("failed to create default backend set %s: %w", defaultBackendSetName, err)
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
		LoadBalancerId: lo.ToPtr(params.loadBalancerID),
	})
	if err != nil {
		return loadbalancer.BackendSet{}, fmt.Errorf("failed to get default backend set %s: %w", defaultBackendSetName, err)
	}

	return res.BackendSet, nil
}

func (m *ociLoadBalancerModelImpl) reconcileListenersCertificates(
	ctx context.Context,
	params reconcileListenersCertificatesParams,
) (reconcileListenersCertificatesResult, error) {
	allRefs := lo.FlatMap(
		params.gateway.Spec.Listeners, func(listener gatewayv1.Listener, _ int) []gatewayv1.SecretObjectReference {
			return listener.TLS.CertificateRefs
		})

	allSecretsByCertName := map[string]v1.Secret{}
	for _, ref := range allRefs {
		secret := v1.Secret{}
		ns := lo.Ternary(ref.Namespace != nil, string(*ref.Namespace), params.gateway.Namespace)
		err := m.k8sClient.Get(ctx, types.NamespacedName{
			Name:      string(ref.Name),
			Namespace: ns,
		}, &secret)
		if err != nil {
			return reconcileListenersCertificatesResult{}, fmt.Errorf("failed to get secret %s: %w", ref.Name, err)
		}
		allSecretsByCertName[ociCertificateNameFromSecret(secret)] = secret
	}

	return reconcileListenersCertificatesResult{
		knownCertificates: params.knownCertificates,
	}, nil
}

func (m *ociLoadBalancerModelImpl) reconcileHTTPListener(
	ctx context.Context,
	params reconcileHTTPListenerParams,
) error {
	listenerName := string(params.listenerSpec.Name)
	if _, ok := params.knownListeners[listenerName]; ok {
		m.logger.DebugContext(ctx, "Listener already exists",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("listenerName", listenerName),
		)
		return nil
	}

	m.logger.InfoContext(ctx, "Listener not found, creating",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("name", listenerName),
	)

	// Create a routing policy first
	routingPolicyName := listenerPolicyName(listenerName)

	if _, ok := params.knownRoutingPolicies[routingPolicyName]; !ok {
		m.logger.InfoContext(ctx, "Creating routing policy for listener",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("routingPolicyName", routingPolicyName),
			slog.String("listenerName", listenerName),
		)

		createRoutingPolicyRes, err := m.ociClient.CreateRoutingPolicy(ctx, loadbalancer.CreateRoutingPolicyRequest{
			LoadBalancerId: &params.loadBalancerID,
			CreateRoutingPolicyDetails: loadbalancer.CreateRoutingPolicyDetails{
				Name:                     lo.ToPtr(routingPolicyName),
				ConditionLanguageVersion: loadbalancer.CreateRoutingPolicyDetailsConditionLanguageVersionV1,
				Rules: []loadbalancer.RoutingRule{
					// We're creating routing policy to have it available when reconciling routes
					// It's not possible to create an empty routing policy, so we're adding a default rule.
					// Alternative could be to create and attach routing policy when reconciling routes, but
					// it may be a bit more complex on the route reconciler side.
					{
						Name:      lo.ToPtr(defaultCatchAllRuleName),
						Condition: lo.ToPtr("any(http.request.url.path sw '/')"),
						Actions: []loadbalancer.Action{
							loadbalancer.ForwardToBackendSet{
								BackendSetName: lo.ToPtr(params.defaultBackendSetName),
							},
						},
					},
				},
			},
		})
		if err != nil {
			return fmt.Errorf("failed to create routing policy %s: %w", routingPolicyName, err)
		}

		if err = m.workRequestsWatcher.WaitFor(
			ctx,
			*createRoutingPolicyRes.OpcWorkRequestId,
		); err != nil {
			return fmt.Errorf("failed to wait for routing policy %s: %w", routingPolicyName, err)
		}
	} else {
		m.logger.DebugContext(ctx, "Routing policy already exists, skipping creation",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("routingPolicyName", routingPolicyName),
			slog.String("listenerName", listenerName),
		)
	}

	// Now create the listener with the routing policy
	createRes, err := m.ociClient.CreateListener(ctx, loadbalancer.CreateListenerRequest{
		LoadBalancerId: &params.loadBalancerID,
		CreateListenerDetails: loadbalancer.CreateListenerDetails{
			Name:                  lo.ToPtr(listenerName),
			DefaultBackendSetName: lo.ToPtr(params.defaultBackendSetName),
			Port:                  lo.ToPtr(int(params.listenerSpec.Port)),
			Protocol:              lo.ToPtr("HTTP"),
			RoutingPolicyName:     lo.ToPtr(routingPolicyName),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create listener %s: %w", listenerName, err)
	}

	if err = m.workRequestsWatcher.WaitFor(
		ctx,
		*createRes.OpcWorkRequestId,
	); err != nil {
		return fmt.Errorf("failed to wait for listener %s: %w", listenerName, err)
	}

	return nil
}

func (m *ociLoadBalancerModelImpl) reconcileBackendSet(
	ctx context.Context,
	params reconcileBackendSetParams,
) error {
	backendSetName := ociBackendSetNameFromService(params.service)

	_, err := m.ociClient.GetBackendSet(ctx, loadbalancer.GetBackendSetRequest{
		BackendSetName: &backendSetName,
		LoadBalancerId: &params.loadBalancerID,
	})
	if err != nil {
		serviceErr, ok := common.IsServiceError(err)
		if !ok || serviceErr.GetHTTPStatusCode() != http.StatusNotFound {
			return fmt.Errorf("failed to get backend set %s: %w", backendSetName, err)
		}
	} else {
		m.logger.DebugContext(ctx, "Backend set found, skipping creation",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("backendSetName", backendSetName),
		)
		return nil
	}

	m.logger.InfoContext(ctx, "Backend set not found, creating",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("backendSetName", backendSetName),
	)

	createRes, err := m.ociClient.CreateBackendSet(ctx, loadbalancer.CreateBackendSetRequest{
		LoadBalancerId: &params.loadBalancerID,
		CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
			Name:   &backendSetName,
			Policy: lo.ToPtr("ROUND_ROBIN"),
			HealthChecker: &loadbalancer.HealthCheckerDetails{
				Protocol: lo.ToPtr("TCP"),
				Port:     lo.ToPtr(int(params.service.Spec.Ports[0].Port)),
			},
		},
	})

	if err != nil {
		return fmt.Errorf("failed to create backend set %s: %w", backendSetName, err)
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
	ruleName := ociListerPolicyRuleName(params.httpRoute, params.httpRouteRuleIndex)
	rule := params.httpRoute.Spec.Rules[params.httpRouteRuleIndex]

	targetBackends := lo.Map(rule.BackendRefs, func(backendRef gatewayv1.HTTPBackendRef, _ int) string {
		return ociBackendSetNameFromBackendRef(params.httpRoute, backendRef)
	})

	condition, err := m.routingRulesMapper.mapHTTPRouteMatchesToCondition(rule.Matches)
	if err != nil {
		return loadbalancer.RoutingRule{}, fmt.Errorf("failed to map http route matches to condition: %w", err)
	}

	m.logger.DebugContext(ctx, "Building OCI routing rule",
		slog.String("httpRoute", fmt.Sprintf("%s/%s", params.httpRoute.Namespace, params.httpRoute.Name)),
		slog.Int("httpRouteRuleIndex", params.httpRouteRuleIndex),
		slog.String("ruleName", ruleName),
		slog.Any("targetBackends", targetBackends),
	)

	return loadbalancer.RoutingRule{
		Name:      lo.ToPtr(ruleName),
		Condition: lo.ToPtr(condition),
		Actions: lo.Map(targetBackends, func(backendSetName string, _ int) loadbalancer.Action {
			return loadbalancer.ForwardToBackendSet{
				BackendSetName: lo.ToPtr(backendSetName),
			}
		}),
	}, nil
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

func (m *ociLoadBalancerModelImpl) commitRoutingPolicy(
	ctx context.Context,
	params commitRoutingPolicyParams,
) error {
	policyName := listenerPolicyName(params.listenerName)

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

	// Sort the rules: defaultCatchAllRuleName should be at the end
	sort.Slice(mergedRules, func(i, j int) bool {
		ruleI := lo.FromPtr(mergedRules[i].Name)
		ruleJ := lo.FromPtr(mergedRules[j].Name)
		if ruleI == defaultCatchAllRuleName {
			return false
		}
		if ruleJ == defaultCatchAllRuleName {
			return true
		}
		return ruleI < ruleJ
	})

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

func listenerPolicyName(listenerName string) string {
	// TODO: Sanitize the name, investigate docs for allowed characters
	return listenerName + "_policy"
}

/*
Name for the routing policy rule set. A name is required. The name must be unique,
and can't be changed. The name can't begin with a period and can't contain any of
these characters: ; ? # / % \ ] [. The name must start with an lower- or upper- case
letter or an underscore, and the rest of the name can contain numbers, underscores,
and upper- or lowercase letters.
*/
var invalidCharsForPolicyNamePattern = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// ociListerPolicyRuleName returns the name of the routing rule for the listener policy.
// It's expected that the rule name is unique within the listener policy for every route.
// Names should also be sortable, so we're using a 4 digit index.
func ociListerPolicyRuleName(route gatewayv1.HTTPRoute, ruleIndex int) string {
	// TODO: This may probably need to have namespace
	// Also check if namespace is populated in the route if it's not in the spec
	// Also mention in docs that policy is per listener and rules for different
	// services best to have something unique like host matching

	rule := route.Spec.Rules[ruleIndex]

	var resultingName string
	if rule.Name != nil {
		resultingName = fmt.Sprintf("p%04d_%s_%s", ruleIndex, route.Name, string(*rule.Name))
	} else {
		resultingName = fmt.Sprintf("p%04d_%s", ruleIndex, route.Name)
	}

	return ociapi.ConstructOCIResourceName(resultingName, ociapi.OCIResourceNameConfig{
		MaxLength:           maxListenerPolicyNameLength,
		InvalidCharsPattern: invalidCharsForPolicyNamePattern,
	})
}

// ociBackendSetName returns the name of the backend set for the route.
// It's expected that the backend set name is unique within the load balancer for every route.
// Sorting is not required, but keeping padding for consistency and readability.
func ociBackendSetNameFromBackendRef(httpRoute gatewayv1.HTTPRoute, backendRef gatewayv1.HTTPBackendRef) string {
	// TODO: Check if namespace is populated in the route if it's not in the spec
	refName := string(backendRef.Name)
	refNamespace := string(lo.FromPtr(backendRef.Namespace))
	if refNamespace == "" {
		refNamespace = httpRoute.Namespace
	}

	originalName := refNamespace + "-" + refName

	return ociapi.ConstructOCIResourceName(originalName, ociapi.OCIResourceNameConfig{
		MaxLength: maxBackendSetNameLength,
	})
}

func ociBackendSetNameFromService(service v1.Service) string {
	originalName := service.Namespace + "-" + service.Name
	return ociapi.ConstructOCIResourceName(originalName, ociapi.OCIResourceNameConfig{
		MaxLength: maxBackendSetNameLength,
	})
}

type ociLoadBalancerModelDeps struct {
	dig.In

	RootLogger          *slog.Logger
	K8sClient           k8sClient
	OciClient           ociLoadBalancerClient
	WorkRequestsWatcher workRequestsWatcher
	RoutingRulesMapper  ociLoadBalancerRoutingRulesMapper
}

func newOciLoadBalancerModel(deps ociLoadBalancerModelDeps) ociLoadBalancerModel {
	return &ociLoadBalancerModelImpl{
		logger:              deps.RootLogger.WithGroup("oci-load-balancer-model"),
		ociClient:           deps.OciClient,
		k8sClient:           deps.K8sClient,
		workRequestsWatcher: deps.WorkRequestsWatcher,
		routingRulesMapper:  deps.RoutingRulesMapper,
	}
}

func ociCertificateNameFromSecret(
	secret v1.Secret,
) string {
	return secret.Namespace + "-" + secret.Name + "-rev-" + secret.ResourceVersion
}
