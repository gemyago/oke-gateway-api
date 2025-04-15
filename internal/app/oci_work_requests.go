package app

import (
	"context"
	"errors"
	"log/slog"

	"go.uber.org/dig"
)

// ociWorkRequestsModel defines the interface for managing OKE work requests.
type ociWorkRequestsModel interface {
	// waitWorkRequest waits for an OCI Work Request to complete.
	waitWorkRequest(ctx context.Context, workRequestId string) error
}

// ociWorkRequestsClient defines the interface for OCI work requests client operations.
// TODO: Define methods needed from the OCI SDK.
type ociWorkRequestsClient interface {
}

type ociWorkRequestsModelImpl struct {
	client ociWorkRequestsClient
	logger *slog.Logger
}

func (m *ociWorkRequestsModelImpl) waitWorkRequest(ctx context.Context, workRequestId string) error {
	// TODO: Implement work request polling logic
	return errors.New("not implemented")
}

type okeWorkRequestsModelDeps struct {
	dig.In

	WorkRequestsClient ociWorkRequestsClient
	RootLogger         *slog.Logger
}

func newOkeWorkRequestsModel(deps okeWorkRequestsModelDeps) ociWorkRequestsModel {
	return &ociWorkRequestsModelImpl{
		client: deps.WorkRequestsClient,
		logger: deps.RootLogger.WithGroup("oke-work-requests-model"),
	}
}
