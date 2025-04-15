package ociapi

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/oracle/oci-go-sdk/v65/workrequests" // Assuming v65, adjust if needed
	"go.uber.org/dig"
)

// workRequestsClient defines the interface for OCI work requests client operations.
type workRequestsClient interface {
	// GetWorkRequest gets the details of a work request.
	GetWorkRequest(
		ctx context.Context,
		request workrequests.GetWorkRequestRequest,
	) (workrequests.GetWorkRequestResponse, error)
}

// WorkRequestsWatcher defines the interface for watching OCI Work Requests.
type WorkRequestsWatcher struct {
	client       workRequestsClient
	logger       *slog.Logger
	pollInterval time.Duration
}

// WaitFor waits for a work request to succeed.
func (w *WorkRequestsWatcher) WaitFor(ctx context.Context, workRequestID string) error {
	request := workrequests.GetWorkRequestRequest{
		WorkRequestId: &workRequestID,
	}

	timer := time.NewTicker(w.pollInterval)
	defer timer.Stop()

	for {
		response, err := w.client.GetWorkRequest(ctx, request)
		if err != nil {
			return fmt.Errorf("failed to get work request %s: %w", workRequestID, err)
		}

		if response.WorkRequest.Status == workrequests.WorkRequestStatusSucceeded {
			return nil
		}

		select {
		case <-timer.C:
		case <-ctx.Done():
			return fmt.Errorf("work request %s timed out: %w", workRequestID, context.Canceled)
		}
	}
}

type WorkRequestsWatcherDeps struct {
	dig.In

	Client     workRequestsClient
	RootLogger *slog.Logger

	pollInterval time.Duration
}

const defaultPollInterval = 2 * time.Second

func NewWorkRequestsWatcher(deps WorkRequestsWatcherDeps) *WorkRequestsWatcher {
	if deps.pollInterval == 0 {
		deps.pollInterval = defaultPollInterval
	}

	return &WorkRequestsWatcher{
		client:       deps.Client,
		logger:       deps.RootLogger.WithGroup("oci-work-requests"),
		pollInterval: deps.pollInterval,
	}
}
