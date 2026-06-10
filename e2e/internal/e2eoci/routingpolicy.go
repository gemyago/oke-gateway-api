package e2eoci

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"

	"github.com/gemyago/oke-gateway-api/e2e/internal/diag"
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
	return waitForRoutingPolicyRuleNames(
		ctx,
		client,
		loadBalancerID,
		listenerName,
		ruleNames,
		opts,
		func(currentRuleNames []string, expectedRuleNames []string) (bool, string) {
			remaining := intersectRuleNames(currentRuleNames, expectedRuleNames)
			if len(remaining) == 0 {
				return true, ""
			}

			return false, fmt.Sprintf("rules still present: %s", strings.Join(remaining, ", "))
		},
		"removal",
	)
}

func WaitForRoutingPolicyRuleNamesPresent(
	ctx context.Context,
	client LoadBalancerClient,
	loadBalancerID string,
	listenerName string,
	ruleNames []string,
	opts *RoutingPolicyWaitOptions,
) error {
	return waitForRoutingPolicyRuleNames(
		ctx,
		client,
		loadBalancerID,
		listenerName,
		ruleNames,
		opts,
		func(currentRuleNames []string, expectedRuleNames []string) (bool, string) {
			missing := missingRuleNames(currentRuleNames, expectedRuleNames)
			if len(missing) == 0 {
				return true, ""
			}

			return false, fmt.Sprintf("rules still missing: %s", strings.Join(missing, ", "))
		},
		"presence",
	)
}

func waitForRoutingPolicyRuleNames(
	ctx context.Context,
	client LoadBalancerClient,
	loadBalancerID string,
	listenerName string,
	ruleNames []string,
	opts *RoutingPolicyWaitOptions,
	done func(currentRuleNames []string, expectedRuleNames []string) (bool, string),
	waitKind string,
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
	description := fmt.Sprintf("wait for routing policy %q rule %s", policyName, waitKind)
	progressLogger := diag.NewWaitProgressLogger(nil, description, 0)
	var lastMessage string

	for {
		currentRuleNames, err := getRoutingPolicyRuleNames(ctx, client, loadBalancerID, policyName)
		if err != nil {
			return fmt.Errorf("%s: %w", description, err)
		}

		finished, message := done(currentRuleNames, ruleNames)
		if finished {
			return nil
		}

		lastMessage = message
		progressLogger.Log(ctx, lastMessage)

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s: %s: %w", description, lastMessage, ctx.Err())
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

func missingRuleNames(currentRuleNames []string, expectedPresentRuleNames []string) []string {
	missing := make([]string, 0, len(expectedPresentRuleNames))
	current := make(map[string]struct{}, len(currentRuleNames))
	for _, ruleName := range currentRuleNames {
		current[ruleName] = struct{}{}
	}

	for _, ruleName := range expectedPresentRuleNames {
		if _, ok := current[ruleName]; ok {
			continue
		}

		missing = append(missing, ruleName)
	}

	slices.Sort(missing)

	return missing
}
