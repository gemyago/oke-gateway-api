package ociapi

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"go.uber.org/dig"
)

// workRequestsClient defines the interface for OCI work requests client operations.
type workRequestsClient interface {
	// GetWorkRequest gets the details of a work request.
	GetWorkRequest(
		ctx context.Context,
		request loadbalancer.GetWorkRequestRequest,
	) (loadbalancer.GetWorkRequestResponse, error)
}

// WorkRequestsWatcher defines the interface for watching OCI Work Requests.
type WorkRequestsWatcher struct {
	client          workRequestsClient
	logger          *slog.Logger
	pollInterval    time.Duration
	maxPollDuration time.Duration
}

// networkLoadBalancerWorkRequestsClient defines OCI NLB work request operations.
type networkLoadBalancerWorkRequestsClient interface {
	GetWorkRequest(
		ctx context.Context,
		request networkloadbalancer.GetWorkRequestRequest,
	) (networkloadbalancer.GetWorkRequestResponse, error)
}

// NetworkLoadBalancerWorkRequestsWatcher watches OCI Network Load Balancer work requests.
type NetworkLoadBalancerWorkRequestsWatcher struct {
	client          networkLoadBalancerWorkRequestsClient
	logger          *slog.Logger
	pollInterval    time.Duration
	maxPollDuration time.Duration
}

type WorkRequestsWatcherDeps struct {
	dig.In `ignore-unexported:"true"`

	Client     workRequestsClient
	RootLogger *slog.Logger

	pollInterval    time.Duration
	maxPollDuration time.Duration
}

type NetworkLoadBalancerWorkRequestsWatcherDeps struct {
	dig.In `ignore-unexported:"true"`

	Client     networkLoadBalancerWorkRequestsClient
	RootLogger *slog.Logger

	pollInterval    time.Duration
	maxPollDuration time.Duration
}

const defaultPollInterval = 2 * time.Second
const defaultMaxPollDuration = 20 * time.Minute

func NewWorkRequestsWatcher(deps WorkRequestsWatcherDeps) *WorkRequestsWatcher {
	if deps.pollInterval == 0 {
		deps.pollInterval = defaultPollInterval
	}

	if deps.maxPollDuration == 0 {
		deps.maxPollDuration = defaultMaxPollDuration
	}

	return &WorkRequestsWatcher{
		client:          deps.Client,
		logger:          deps.RootLogger.WithGroup("oci-work-requests"),
		pollInterval:    deps.pollInterval,
		maxPollDuration: deps.maxPollDuration,
	}
}

func NewNetworkLoadBalancerWorkRequestsWatcher(
	deps NetworkLoadBalancerWorkRequestsWatcherDeps,
) *NetworkLoadBalancerWorkRequestsWatcher {
	if deps.pollInterval == 0 {
		deps.pollInterval = defaultPollInterval
	}

	if deps.maxPollDuration == 0 {
		deps.maxPollDuration = defaultMaxPollDuration
	}

	return &NetworkLoadBalancerWorkRequestsWatcher{
		client:          deps.Client,
		logger:          deps.RootLogger.WithGroup("oci-network-load-balancer-work-requests"),
		pollInterval:    deps.pollInterval,
		maxPollDuration: deps.maxPollDuration,
	}
}

// WaitFor waits for a work request to succeed.
func (w *WorkRequestsWatcher) WaitFor(ctx context.Context, workRequestID string) error {
	request := loadbalancer.GetWorkRequestRequest{
		WorkRequestId: &workRequestID,
	}

	return waitForWorkRequest(ctx, workRequestWaitConfig{
		logger:          w.logger,
		workRequestID:   workRequestID,
		pollInterval:    w.pollInterval,
		maxPollDuration: w.maxPollDuration,
		description:     "work request",
		getStatus: func() (string, bool, bool, error) {
			response, err := w.client.GetWorkRequest(ctx, request)
			if err != nil {
				return "", false, false, fmt.Errorf("failed to get work request %s: %w", workRequestID, err)
			}
			status := string(response.WorkRequest.LifecycleState)
			return status,
				response.WorkRequest.LifecycleState == loadbalancer.WorkRequestLifecycleStateSucceeded,
				response.WorkRequest.LifecycleState == loadbalancer.WorkRequestLifecycleStateFailed,
				nil
		},
	})
}

// WaitFor waits for an OCI Network Load Balancer work request to succeed.
func (w *NetworkLoadBalancerWorkRequestsWatcher) WaitFor(ctx context.Context, workRequestID string) error {
	request := networkloadbalancer.GetWorkRequestRequest{
		WorkRequestId: &workRequestID,
	}

	return waitForWorkRequest(ctx, workRequestWaitConfig{
		logger:          w.logger,
		workRequestID:   workRequestID,
		pollInterval:    w.pollInterval,
		maxPollDuration: w.maxPollDuration,
		description:     "network load balancer work request",
		getStatus: func() (string, bool, bool, error) {
			response, err := w.client.GetWorkRequest(ctx, request)
			if err != nil {
				return "", false, false, fmt.Errorf(
					"failed to get network load balancer work request %s: %w",
					workRequestID,
					err,
				)
			}
			status := string(response.WorkRequest.Status)
			return status,
				response.WorkRequest.Status == networkloadbalancer.OperationStatusSucceeded,
				response.WorkRequest.Status == networkloadbalancer.OperationStatusFailed,
				nil
		},
	})
}

type workRequestWaitConfig struct {
	logger          *slog.Logger
	workRequestID   string
	pollInterval    time.Duration
	maxPollDuration time.Duration
	description     string
	getStatus       func() (status string, succeeded bool, failed bool, err error)
}

func waitForWorkRequest(ctx context.Context, config workRequestWaitConfig) error {
	intervalTicker := time.NewTicker(config.pollInterval)
	defer intervalTicker.Stop()

	deadlineTicker := time.NewTimer(config.maxPollDuration)
	defer deadlineTicker.Stop()

	for {
		status, succeeded, failed, err := config.getStatus()
		if err != nil {
			return err
		}
		if succeeded {
			return nil
		}
		if failed {
			return fmt.Errorf("%s %s is in %s state", config.description, config.workRequestID, status)
		}

		config.logger.DebugContext(
			ctx, config.description+" is in progress",
			slog.String("workRequestID", config.workRequestID),
			slog.String("status", status),
		)

		select {
		case <-intervalTicker.C:
		case <-deadlineTicker.C:
			return fmt.Errorf("%s %s timed out: %w", config.description, config.workRequestID, context.DeadlineExceeded)
		case <-ctx.Done():
			return fmt.Errorf("%s %s timed out: %w", config.description, config.workRequestID, context.Canceled)
		}
	}
}
