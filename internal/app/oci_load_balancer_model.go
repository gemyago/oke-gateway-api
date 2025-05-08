package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const defaultBackendSetPort = 80
const defaultCatchAllRuleName = "default_catch_all"

type reconcileDefaultBackendParams struct {
	loadBalancerID   string
	knownBackendSets map[string]loadbalancer.BackendSet
	gateway          *gatewayv1.Gateway
}

type reconcileBackendSetParams struct {
	loadBalancerID string
	name           string
	healthChecker  *loadbalancer.HealthCheckerDetails
}

type reconcileHTTPListenerParams struct {
	loadBalancerID        string
	knownListeners        map[string]loadbalancer.Listener
	defaultBackendSetName string
	listenerSpec          *gatewayv1.Listener
}

type resolveAndTidyRoutingPolicyParams struct {
	loadBalancerID string
	policyName     string
	httpRoute      gatewayv1.HTTPRoute
}

type appendRoutingRuleParams struct {
	actualPolicyRules  []loadbalancer.RoutingRule
	httpRoute          gatewayv1.HTTPRoute
	httpRouteRuleIndex int
}

type commitRoutingPolicyParams struct {
	loadBalancerID string
	policy         loadbalancer.RoutingPolicy
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
	reconcileHTTPListener(
		ctx context.Context,
		params reconcileHTTPListenerParams,
	) error

	// TODO: It may not need to return the backend set
	// review and update
	reconcileBackendSet(
		ctx context.Context,
		params reconcileBackendSetParams,
	) error

	// resolveAndTidyRoutingPolicy removes rules from the routing policy that are
	// associated with the given httpRoute. It is expected that consumer will repopulate
	// new rules related to the given httpRoute.
	resolveAndTidyRoutingPolicy(
		ctx context.Context,
		params resolveAndTidyRoutingPolicyParams,
	) (loadbalancer.RoutingPolicy, error)

	// appendRoutingRule appends a new routing rule to the routing policy.
	appendRoutingRule(
		ctx context.Context,
		params appendRoutingRuleParams,
	) ([]loadbalancer.RoutingRule, error)

	commitRoutingPolicy(
		ctx context.Context,
		params commitRoutingPolicyParams,
	) error

	// removeMissingListeners removes listeners from the load balancer that are not present in the gateway spec.
	removeMissingListeners(ctx context.Context, params removeMissingListenersParams) error
}

type ociLoadBalancerModelImpl struct {
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

	// Now create the listener with the routing policy
	createRes, err := m.ociClient.CreateListener(ctx, loadbalancer.CreateListenerRequest{
		LoadBalancerId: &params.loadBalancerID,
		CreateListenerDetails: loadbalancer.CreateListenerDetails{
			Name:                  lo.ToPtr(listenerName),
			DefaultBackendSetName: lo.ToPtr(params.defaultBackendSetName),
			Port:                  lo.ToPtr(int(params.listenerSpec.Port)),
			Protocol:              lo.ToPtr(string(params.listenerSpec.Protocol)),
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
	m.logger.InfoContext(ctx, "Reconciling backend set",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("backendSetName", params.name),
	)

	existingBsFound := true
	_, err := m.ociClient.GetBackendSet(ctx, loadbalancer.GetBackendSetRequest{
		BackendSetName: &params.name,
		LoadBalancerId: &params.loadBalancerID,
	})
	if err != nil {
		serviceErr, ok := common.IsServiceError(err)
		if !ok || serviceErr.GetHTTPStatusCode() != http.StatusNotFound {
			return fmt.Errorf("failed to get backend set %s: %w", params.name, err)
		}
		existingBsFound = false
	}

	if existingBsFound {
		m.logger.DebugContext(ctx, "Backend set found",
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.String("backendSetName", params.name),
		)

		// TODO: Logic to update backend set

		return nil
	}

	m.logger.DebugContext(ctx, "Backend set not found, creating",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("backendSetName", params.name),
	)

	createRes, err := m.ociClient.CreateBackendSet(ctx, loadbalancer.CreateBackendSetRequest{
		LoadBalancerId: lo.ToPtr(params.loadBalancerID),
		CreateBackendSetDetails: loadbalancer.CreateBackendSetDetails{
			Name:          &params.name,
			HealthChecker: params.healthChecker,
			Policy:        lo.ToPtr("ROUND_ROBIN"),
		},
	})

	if err != nil {
		return fmt.Errorf("failed to create backend set %s: %w", params.name, err)
	}

	if err = m.workRequestsWatcher.WaitFor(
		ctx,
		*createRes.OpcWorkRequestId,
	); err != nil {
		return fmt.Errorf("failed to wait for backend set %s: %w", params.name, err)
	}

	return nil
}

func (m *ociLoadBalancerModelImpl) resolveAndTidyRoutingPolicy(
	ctx context.Context,
	params resolveAndTidyRoutingPolicyParams,
) (loadbalancer.RoutingPolicy, error) {
	policyResponse, err := m.ociClient.GetRoutingPolicy(ctx, loadbalancer.GetRoutingPolicyRequest{
		RoutingPolicyName: &params.policyName,
		LoadBalancerId:    &params.loadBalancerID,
	})
	if err != nil {
		return loadbalancer.RoutingPolicy{}, fmt.Errorf("failed to get routing policy %s: %w", params.policyName, err)
	}

	// All rules associated with the current httpRoute will have a name starting with this prefix.
	rulesPrefixToRemove := params.httpRoute.Name + "_"

	cleanedRules := lo.Filter(policyResponse.RoutingPolicy.Rules,
		func(rule loadbalancer.RoutingRule, _ int) bool {
			ruleName := lo.FromPtr(rule.Name)
			return !strings.HasPrefix(ruleName, rulesPrefixToRemove)
		})

	cleanedPolicy := policyResponse.RoutingPolicy
	cleanedPolicy.Rules = cleanedRules

	return cleanedPolicy, nil
}

func (m *ociLoadBalancerModelImpl) appendRoutingRule(
	ctx context.Context,
	params appendRoutingRuleParams,
) ([]loadbalancer.RoutingRule, error) {
	ruleName := ociListerPolicyRuleName(params.httpRoute, params.httpRouteRuleIndex)
	backendSetName := ociBackendSetName(params.httpRoute, params.httpRouteRuleIndex)

	m.logger.DebugContext(ctx, "Adding OCI routing rule",
		slog.String("httpRoute", fmt.Sprintf("%s/%s", params.httpRoute.Namespace, params.httpRoute.Name)),
		slog.Int("httpRouteRuleIndex", params.httpRouteRuleIndex),
		slog.String("ruleName", ruleName),
		slog.String("backendSetName", backendSetName),
	)

	ruleSpec := params.httpRoute.Spec.Rules[params.httpRouteRuleIndex]

	condition, err := m.routingRulesMapper.mapHTTPRouteMatchesToCondition(ruleSpec.Matches)
	if err != nil {
		return nil, fmt.Errorf("failed to map http route matches to condition: %w", err)
	}

	newRule := loadbalancer.RoutingRule{
		Name:      lo.ToPtr(ruleName),
		Condition: lo.ToPtr(condition),
		Actions: []loadbalancer.Action{
			loadbalancer.ForwardToBackendSet{
				BackendSetName: lo.ToPtr(backendSetName),
			},
		},
	}

	return append(params.actualPolicyRules, newRule), nil
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
	updateRes, err := m.ociClient.UpdateRoutingPolicy(ctx, loadbalancer.UpdateRoutingPolicyRequest{
		LoadBalancerId:    &params.loadBalancerID,
		RoutingPolicyName: params.policy.Name,
		UpdateRoutingPolicyDetails: loadbalancer.UpdateRoutingPolicyDetails{
			ConditionLanguageVersion: loadbalancer.UpdateRoutingPolicyDetailsConditionLanguageVersionEnum(
				params.policy.ConditionLanguageVersion,
			),
			Rules: params.policy.Rules,
		},
	})
	if err != nil {
		m.logger.WarnContext(ctx, "Failed to update routing policy",
			diag.ErrAttr(err),
			slog.String("loadBalancerId", params.loadBalancerID),
			slog.Any("policy", params.policy),
		)
		return fmt.Errorf("failed to update routing policy %s: %w", *params.policy.Name, err)
	}

	if err = m.workRequestsWatcher.WaitFor(
		ctx,
		*updateRes.OpcWorkRequestId,
	); err != nil {
		return fmt.Errorf("failed to wait for routing policy %s update: %w", *params.policy.Name, err)
	}

	m.logger.InfoContext(ctx, "Successfully committed routing policy changes",
		slog.String("loadBalancerId", params.loadBalancerID),
		slog.String("routingPolicyName", *params.policy.Name),
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
	rule := route.Spec.Rules[ruleIndex]

	var resultingName string
	if rule.Name != nil {
		resultingName = fmt.Sprintf("%s_r%04d_%s", route.Name, ruleIndex, string(*rule.Name))
	} else {
		resultingName = fmt.Sprintf("%s_r%04d", route.Name, ruleIndex)
	}

	// sanitize the name using the pattern
	resultingName = invalidCharsForPolicyNamePattern.ReplaceAllString(resultingName, "_")

	return resultingName
}

// ociBackendSetName returns the name of the backend set for the route.
// It's expected that the backend set name is unique within the load balancer for every route.
// Sorting is not required, but keeping padding for consistency and readability.
func ociBackendSetName(httpRoute gatewayv1.HTTPRoute, ruleIndex int) string {
	// TODO: This may probably need to have namespace
	// Also check if namespace is populated in the route if it's not in the spec
	rule := httpRoute.Spec.Rules[ruleIndex]
	if rule.Name != nil {
		return fmt.Sprintf("%s-r%04d-%s", httpRoute.Name, ruleIndex, string(*rule.Name))
	}

	return fmt.Sprintf("%s-r%04d", httpRoute.Name, ruleIndex)
}

type ociLoadBalancerModelDeps struct {
	dig.In

	RootLogger          *slog.Logger
	OciClient           ociLoadBalancerClient
	WorkRequestsWatcher workRequestsWatcher
	RoutingRulesMapper  ociLoadBalancerRoutingRulesMapper
}

func newOciLoadBalancerModel(deps ociLoadBalancerModelDeps) ociLoadBalancerModel {
	return &ociLoadBalancerModelImpl{
		logger:              deps.RootLogger.WithGroup("oci-load-balancer-model"),
		ociClient:           deps.OciClient,
		workRequestsWatcher: deps.WorkRequestsWatcher,
		routingRulesMapper:  deps.RoutingRulesMapper,
	}
}
