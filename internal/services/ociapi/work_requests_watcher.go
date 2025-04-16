package ociapi

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer" // Assuming v65, adjust if needed
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

// WaitFor waits for a work request to succeed.
func (w *WorkRequestsWatcher) WaitFor(ctx context.Context, workRequestID string) error {
	request := loadbalancer.GetWorkRequestRequest{
		WorkRequestId: &workRequestID,
	}

	intervalTicker := time.NewTicker(w.pollInterval)
	defer intervalTicker.Stop()

	deadlineTicker := time.NewTimer(w.maxPollDuration)
	defer deadlineTicker.Stop()

	for {
		response, err := w.client.GetWorkRequest(ctx, request)
		if err != nil {
			return fmt.Errorf("failed to get work request %s: %w", workRequestID, err)
		}

		if response.WorkRequest.LifecycleState == loadbalancer.WorkRequestLifecycleStateSucceeded {
			return nil
		}

		if response.WorkRequest.LifecycleState == loadbalancer.WorkRequestLifecycleStateFailed {
			return fmt.Errorf("work request %s is in %s state", workRequestID, response.WorkRequest.LifecycleState)
		}

		w.logger.DebugContext(
			ctx, "work request is in progress",
			slog.String("workRequestID", workRequestID),
			slog.String("status", string(response.WorkRequest.LifecycleState)),
		)

		select {
		case <-intervalTicker.C:
		case <-deadlineTicker.C:
			return fmt.Errorf("work request %s timed out: %w", workRequestID, context.DeadlineExceeded)
		case <-ctx.Done():
			return fmt.Errorf("work request %s timed out: %w", workRequestID, context.Canceled)
		}
	}
}

type WorkRequestsWatcherDeps struct {
	dig.In `ignore-unexported:"true"`

	Client     workRequestsClient
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
