package e2eoci

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
)

const (
	defaultWorkRequestPollInterval = 2 * time.Second
	defaultWorkRequestWaitTimeout  = 20 * time.Minute
)

type workRequestClient interface {
	GetWorkRequest(
		context.Context,
		loadbalancer.GetWorkRequestRequest,
	) (loadbalancer.GetWorkRequestResponse, error)
}

type WorkRequestWaiterOptions struct {
	PollInterval time.Duration
	WaitTimeout  time.Duration
}

type WorkRequestWaiter struct {
	client       workRequestClient
	logger       *slog.Logger
	pollInterval time.Duration
	waitTimeout  time.Duration
}

func NewWorkRequestWaiter(
	client workRequestClient,
	logger *slog.Logger,
	opts *WorkRequestWaiterOptions,
) *WorkRequestWaiter {
	if opts == nil {
		opts = &WorkRequestWaiterOptions{}
	}

	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultWorkRequestPollInterval
	}

	waitTimeout := opts.WaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = defaultWorkRequestWaitTimeout
	}

	if logger == nil {
		logger = slog.Default()
	}

	return &WorkRequestWaiter{
		client:       client,
		logger:       logger,
		pollInterval: pollInterval,
		waitTimeout:  waitTimeout,
	}
}

func (w *WorkRequestWaiter) Wait(ctx context.Context, workRequestID string) error {
	workRequestID = strings.TrimSpace(workRequestID)
	if workRequestID == "" {
		return errors.New("work request id is required")
	}

	waitCtx, cancel := context.WithTimeout(ctx, w.waitTimeout)
	defer cancel()

	request := loadbalancer.GetWorkRequestRequest{
		WorkRequestId: &workRequestID,
	}

	for {
		response, err := w.client.GetWorkRequest(waitCtx, request)
		if err != nil {
			return fmt.Errorf("get work request %q: %w", workRequestID, err)
		}

		switch response.WorkRequest.LifecycleState {
		case loadbalancer.WorkRequestLifecycleStateAccepted,
			loadbalancer.WorkRequestLifecycleStateInProgress:
		case loadbalancer.WorkRequestLifecycleStateSucceeded:
			return nil
		case loadbalancer.WorkRequestLifecycleStateFailed:
			return fmt.Errorf(
				"work request %q failed: %s",
				workRequestID,
				describeWorkRequestFailure(response.WorkRequest),
			)
		}

		w.logger.DebugContext(
			waitCtx,
			"waiting for OCI work request",
			slog.String("workRequestID", workRequestID),
			slog.String("lifecycleState", string(response.WorkRequest.LifecycleState)),
		)

		sleepErr := sleepContext(waitCtx, w.pollInterval)
		if sleepErr != nil {
			if errors.Is(sleepErr, context.DeadlineExceeded) {
				return fmt.Errorf(
					"work request %q timed out after %s: %w",
					workRequestID,
					w.waitTimeout,
					sleepErr,
				)
			}

			return fmt.Errorf("work request %q canceled: %w", workRequestID, sleepErr)
		}
	}
}

func describeWorkRequestFailure(workRequest loadbalancer.WorkRequest) string {
	var parts []string

	if workRequest.Message != nil && strings.TrimSpace(*workRequest.Message) != "" {
		parts = append(parts, strings.TrimSpace(*workRequest.Message))
	}

	for _, detail := range workRequest.ErrorDetails {
		if detail.Message == nil || strings.TrimSpace(*detail.Message) == "" {
			continue
		}

		if detail.ErrorCode == "" {
			parts = append(parts, strings.TrimSpace(*detail.Message))
			continue
		}

		parts = append(parts, fmt.Sprintf("%s: %s", detail.ErrorCode, strings.TrimSpace(*detail.Message)))
	}

	if len(parts) == 0 {
		return fmt.Sprintf("lifecycle state %s", workRequest.LifecycleState)
	}

	return strings.Join(parts, "; ")
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
