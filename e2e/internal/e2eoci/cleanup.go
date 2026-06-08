package e2eoci

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
)

type LoadBalancerClient interface {
	GetLoadBalancer(
		context.Context,
		loadbalancer.GetLoadBalancerRequest,
	) (loadbalancer.GetLoadBalancerResponse, error)
	GetRoutingPolicy(
		context.Context,
		loadbalancer.GetRoutingPolicyRequest,
	) (loadbalancer.GetRoutingPolicyResponse, error)
	DeleteListener(
		context.Context,
		loadbalancer.DeleteListenerRequest,
	) (loadbalancer.DeleteListenerResponse, error)
	DeleteRoutingPolicy(
		context.Context,
		loadbalancer.DeleteRoutingPolicyRequest,
	) (loadbalancer.DeleteRoutingPolicyResponse, error)
	DeleteBackendSet(
		context.Context,
		loadbalancer.DeleteBackendSetRequest,
	) (loadbalancer.DeleteBackendSetResponse, error)
	GetWorkRequest(
		context.Context,
		loadbalancer.GetWorkRequestRequest,
	) (loadbalancer.GetWorkRequestResponse, error)
}

type LoadBalancerCleanerOptions struct {
	WorkRequestPollInterval time.Duration
	WorkRequestWaitTimeout  time.Duration
}

type DisposableLoadBalancer struct {
	ID                 string
	PublicIP           string
	LifecycleState     loadbalancer.LoadBalancerLifecycleStateEnum
	ListenerNames      []string
	RoutingPolicyNames []string
	BackendSetNames    []string
}

type CleanupResult struct {
	DisposableLoadBalancer

	DeletedListeners       []string
	DeletedRoutingPolicies []string
	DeletedBackendSets     []string
}

type LoadBalancerCleaner struct {
	client LoadBalancerClient
	waiter *WorkRequestWaiter
	logger *slog.Logger
}

func NewLoadBalancerCleaner(
	client LoadBalancerClient,
	logger *slog.Logger,
	opts *LoadBalancerCleanerOptions,
) *LoadBalancerCleaner {
	if opts == nil {
		opts = &LoadBalancerCleanerOptions{}
	}

	if logger == nil {
		logger = slog.Default()
	}

	waiter := NewWorkRequestWaiter(client, logger, &WorkRequestWaiterOptions{
		PollInterval: opts.WorkRequestPollInterval,
		WaitTimeout:  opts.WorkRequestWaitTimeout,
	})

	return &LoadBalancerCleaner{
		client: client,
		waiter: waiter,
		logger: logger,
	}
}

func (c *LoadBalancerCleaner) Inspect(
	ctx context.Context,
	loadBalancerID string,
) (*DisposableLoadBalancer, error) {
	loadBalancerID = strings.TrimSpace(loadBalancerID)
	if loadBalancerID == "" {
		return nil, errors.New("load balancer id is required")
	}

	response, err := c.client.GetLoadBalancer(ctx, loadbalancer.GetLoadBalancerRequest{
		LoadBalancerId: &loadBalancerID,
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil, fmt.Errorf("load balancer %q not found: %w", loadBalancerID, err)
		}

		return nil, fmt.Errorf("get load balancer %q: %w", loadBalancerID, err)
	}

	publicIP, err := selectStablePublicIP(response.LoadBalancer)
	if err != nil {
		return nil, fmt.Errorf("load balancer %q failed preflight validation: %w", loadBalancerID, err)
	}

	return &DisposableLoadBalancer{
		ID:                 loadBalancerID,
		PublicIP:           publicIP,
		LifecycleState:     response.LoadBalancer.LifecycleState,
		ListenerNames:      sortedKeys(response.LoadBalancer.Listeners),
		RoutingPolicyNames: sortedKeys(response.LoadBalancer.RoutingPolicies),
		BackendSetNames:    sortedKeys(response.LoadBalancer.BackendSets),
	}, nil
}

func (c *LoadBalancerCleaner) Cleanup(
	ctx context.Context,
	loadBalancerID string,
) (*CleanupResult, error) {
	disposableLoadBalancer, err := c.Inspect(ctx, loadBalancerID)
	if err != nil {
		return nil, err
	}

	result := &CleanupResult{
		DisposableLoadBalancer: *disposableLoadBalancer,
	}

	c.logger.InfoContext(
		ctx,
		"resetting disposable load balancer children",
		slog.String("publicIP", disposableLoadBalancer.PublicIP),
		slog.String("loadBalancerID", loadBalancerID),
	)

	c.logger.InfoContext(ctx, "deleting listeners", slog.Any("listenerNames", disposableLoadBalancer.ListenerNames))
	deletedListeners, err := c.deleteListeners(ctx, loadBalancerID, disposableLoadBalancer.ListenerNames)
	if err != nil {
		return nil, err
	}
	result.DeletedListeners = deletedListeners

	c.logger.InfoContext(
		ctx,
		"deleting routing policies",
		slog.Any("routingPolicyNames", disposableLoadBalancer.RoutingPolicyNames),
	)
	deletedRoutingPolicies, err := c.deleteRoutingPolicies(
		ctx,
		loadBalancerID,
		disposableLoadBalancer.RoutingPolicyNames,
	)
	if err != nil {
		return nil, err
	}
	result.DeletedRoutingPolicies = deletedRoutingPolicies

	c.logger.InfoContext(
		ctx,
		"deleting backend sets",
		slog.Any("backendSetNames", disposableLoadBalancer.BackendSetNames),
	)
	deletedBackendSets, err := c.deleteBackendSets(ctx, loadBalancerID, disposableLoadBalancer.BackendSetNames)
	if err != nil {
		return nil, err
	}
	result.DeletedBackendSets = deletedBackendSets

	return result, nil
}

func (c *LoadBalancerCleaner) deleteListeners(
	ctx context.Context,
	loadBalancerID string,
	listenerNames []string,
) ([]string, error) {
	deleted := make([]string, 0, len(listenerNames))

	for _, listenerName := range listenerNames {
		response, deleteErr := c.client.DeleteListener(ctx, loadbalancer.DeleteListenerRequest{
			LoadBalancerId: &loadBalancerID,
			ListenerName:   &listenerName,
		})
		if deleteErr != nil {
			if isNotFoundError(deleteErr) {
				c.logger.InfoContext(ctx, "listener already absent", slog.String("listenerName", listenerName))
				continue
			}

			return nil, fmt.Errorf("delete listener %q: %w", listenerName, deleteErr)
		}

		waitErr := c.waitForDeletion(ctx, "listener", listenerName, response.OpcWorkRequestId)
		if waitErr != nil {
			return nil, waitErr
		}

		deleted = append(deleted, listenerName)
	}

	return deleted, nil
}

func (c *LoadBalancerCleaner) deleteRoutingPolicies(
	ctx context.Context,
	loadBalancerID string,
	routingPolicyNames []string,
) ([]string, error) {
	deleted := make([]string, 0, len(routingPolicyNames))

	for _, routingPolicyName := range routingPolicyNames {
		response, deleteErr := c.client.DeleteRoutingPolicy(ctx, loadbalancer.DeleteRoutingPolicyRequest{
			LoadBalancerId:    &loadBalancerID,
			RoutingPolicyName: &routingPolicyName,
		})
		if deleteErr != nil {
			if isNotFoundError(deleteErr) {
				c.logger.InfoContext(
					ctx,
					"routing policy already absent",
					slog.String("routingPolicyName", routingPolicyName),
				)
				continue
			}

			return nil, fmt.Errorf("delete routing policy %q: %w", routingPolicyName, deleteErr)
		}

		waitErr := c.waitForDeletion(ctx, "routing policy", routingPolicyName, response.OpcWorkRequestId)
		if waitErr != nil {
			return nil, waitErr
		}

		deleted = append(deleted, routingPolicyName)
	}

	return deleted, nil
}

func (c *LoadBalancerCleaner) deleteBackendSets(
	ctx context.Context,
	loadBalancerID string,
	backendSetNames []string,
) ([]string, error) {
	deleted := make([]string, 0, len(backendSetNames))

	for _, backendSetName := range backendSetNames {
		response, deleteErr := c.client.DeleteBackendSet(ctx, loadbalancer.DeleteBackendSetRequest{
			LoadBalancerId: &loadBalancerID,
			BackendSetName: &backendSetName,
		})
		if deleteErr != nil {
			if isNotFoundError(deleteErr) {
				c.logger.InfoContext(ctx, "backend set already absent", slog.String("backendSetName", backendSetName))
				continue
			}

			return nil, fmt.Errorf("delete backend set %q: %w", backendSetName, deleteErr)
		}

		waitErr := c.waitForDeletion(ctx, "backend set", backendSetName, response.OpcWorkRequestId)
		if waitErr != nil {
			return nil, waitErr
		}

		deleted = append(deleted, backendSetName)
	}

	return deleted, nil
}

func (c *LoadBalancerCleaner) waitForDeletion(
	ctx context.Context,
	resourceKind string,
	resourceName string,
	workRequestID *string,
) error {
	id := strings.TrimSpace(stringValue(workRequestID))
	if id == "" {
		return fmt.Errorf("%s %q deletion returned no work request id", resourceKind, resourceName)
	}

	if err := c.waiter.Wait(ctx, id); err != nil {
		return fmt.Errorf("wait for %s %q deletion: %w", resourceKind, resourceName, err)
	}

	return nil
}

func selectStablePublicIP(loadBalancer loadbalancer.LoadBalancer) (string, error) {
	publicIPs := make([]string, 0, len(loadBalancer.IpAddresses))
	seen := make(map[string]struct{}, len(loadBalancer.IpAddresses))

	for _, address := range loadBalancer.IpAddresses {
		ip := strings.TrimSpace(stringValue(address.IpAddress))
		if ip == "" {
			continue
		}

		if !isPublicIPAddress(loadBalancer, address) {
			continue
		}

		if _, ok := seen[ip]; ok {
			continue
		}

		seen[ip] = struct{}{}
		publicIPs = append(publicIPs, ip)
	}

	if len(publicIPs) == 0 {
		return "", errors.New("load balancer has no public IP addresses")
	}

	slices.Sort(publicIPs)
	return publicIPs[0], nil
}

func isPublicIPAddress(loadBalancer loadbalancer.LoadBalancer, address loadbalancer.IpAddress) bool {
	if address.IsPublic != nil {
		return *address.IsPublic
	}

	if loadBalancer.IsPrivate != nil {
		return !*loadBalancer.IsPrivate
	}

	return false
}

func sortedKeys[T any](items map[string]T) []string {
	if len(items) == 0 {
		return nil
	}

	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}

	slices.Sort(keys)
	return keys
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

func isNotFoundError(err error) bool {
	serviceErr, ok := common.IsServiceError(err)
	return ok && serviceErr.GetHTTPStatusCode() == 404
}
