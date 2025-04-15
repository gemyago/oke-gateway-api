package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/oracle/oci-go-sdk/v65/workrequests" // Assuming v65, adjust if needed
	"go.uber.org/dig"
)

// ociWorkRequests defines the interface for managing OKE work requests.
type ociWorkRequests interface {
	// waitWorkRequest waits for an OCI Work Request to complete.
	waitWorkRequest(ctx context.Context, workRequestID string) error
}

// ociWorkRequestsClient defines the interface for OCI work requests client operations.
type ociWorkRequestsClient interface {
	// GetWorkRequest gets the details of a work request.
	GetWorkRequest(
		ctx context.Context,
		request workrequests.GetWorkRequestRequest,
	) (workrequests.GetWorkRequestResponse, error)
}

type ociWorkRequestsImpl struct {
	client ociWorkRequestsClient
	logger *slog.Logger
}

func (m *ociWorkRequestsImpl) waitWorkRequest(ctx context.Context, workRequestId string) error {
	// TODO: Implement work request polling logic
	return errors.New("not implemented")
}

type okeWorkRequestsDeps struct {
	dig.In

	OCIClient  ociWorkRequestsClient
	RootLogger *slog.Logger
}

func newOkeWorkRequests(deps okeWorkRequestsDeps) ociWorkRequests {
	return &ociWorkRequestsImpl{
		client: deps.OCIClient,
		logger: deps.RootLogger.WithGroup("oke-work-requests-model"),
	}
}
