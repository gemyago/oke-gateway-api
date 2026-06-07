package e2eoci

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
)

const defaultRoutingPolicyPollInterval = 2 * time.Second

type RoutingPolicyWaitOptions struct {
	PollInterval time.Duration
}

func WaitForRoutingPolicyRuleNamesAbsent(
	ctx context.Context,
	client LoadBalancerClient,
	loadBalancerID string,
	listenerName string,
	ruleNames []string,
	opts *RoutingPolicyWaitOptions,
) error {
	loadBalancerID = strings.TrimSpace(loadBalancerID)
	if loadBalancerID == "" {
		return errors.New("load balancer id is required")
	}

	listenerName = strings.TrimSpace(listenerName)
	if listenerName == "" {
		return errors.New("listener name is required")
	}

	ruleNames = compactRuleNames(ruleNames)
	if len(ruleNames) == 0 {
		return errors.New("at least one rule name is required")
	}

	pollInterval := defaultRoutingPolicyPollInterval
	if opts != nil && opts.PollInterval > 0 {
		pollInterval = opts.PollInterval
	}

	policyName := listenerRoutingPolicyName(listenerName)
	var lastMessage string

	for {
		currentRuleNames, err := getRoutingPolicyRuleNames(ctx, client, loadBalancerID, policyName)
		if err != nil {
			return fmt.Errorf("wait for routing policy %q rule removal: %w", policyName, err)
		}

		remaining := intersectRuleNames(currentRuleNames, ruleNames)
		if len(remaining) == 0 {
			return nil
		}

		lastMessage = fmt.Sprintf("rules still present: %s", strings.Join(remaining, ", "))

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf(
				"wait for routing policy %q rule removal: %s: %w",
				policyName,
				lastMessage,
				ctx.Err(),
			)
		case <-timer.C:
		}
	}
}

func getRoutingPolicyRuleNames(
	ctx context.Context,
	client LoadBalancerClient,
	loadBalancerID string,
	policyName string,
) ([]string, error) {
	response, err := client.GetRoutingPolicy(ctx, loadbalancer.GetRoutingPolicyRequest{
		LoadBalancerId:    &loadBalancerID,
		RoutingPolicyName: &policyName,
	})
	if err != nil {
		return nil, fmt.Errorf("get routing policy %q: %w", policyName, err)
	}

	ruleNames := make([]string, 0, len(response.RoutingPolicy.Rules))
	for _, rule := range response.RoutingPolicy.Rules {
		if rule.Name == nil {
			continue
		}

		name := strings.TrimSpace(*rule.Name)
		if name == "" {
			continue
		}

		ruleNames = append(ruleNames, name)
	}

	slices.Sort(ruleNames)

	return ruleNames, nil
}

func listenerRoutingPolicyName(listenerName string) string {
	return listenerName + "_policy"
}

func compactRuleNames(ruleNames []string) []string {
	compacted := make([]string, 0, len(ruleNames))
	seen := make(map[string]struct{}, len(ruleNames))

	for _, ruleName := range ruleNames {
		ruleName = strings.TrimSpace(ruleName)
		if ruleName == "" {
			continue
		}

		if _, ok := seen[ruleName]; ok {
			continue
		}

		compacted = append(compacted, ruleName)
		seen[ruleName] = struct{}{}
	}

	slices.Sort(compacted)

	return compacted
}

func intersectRuleNames(currentRuleNames []string, expectedAbsentRuleNames []string) []string {
	remaining := make([]string, 0, len(expectedAbsentRuleNames))
	current := make(map[string]struct{}, len(currentRuleNames))
	for _, ruleName := range currentRuleNames {
		current[ruleName] = struct{}{}
	}

	for _, ruleName := range expectedAbsentRuleNames {
		if _, ok := current[ruleName]; ok {
			remaining = append(remaining, ruleName)
		}
	}

	slices.Sort(remaining)

	return remaining
}
