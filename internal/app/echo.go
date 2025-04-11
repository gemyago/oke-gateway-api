package app

import (
	"context"
	"log/slog"

	"go.uber.org/dig"
)

// Minimalistic application layer service example.

type EchoData struct {
	Message string
}

type EchoServiceDeps struct {
	dig.In

	RootLogger *slog.Logger
}

type EchoService struct {
	logger *slog.Logger
}

func (svc *EchoService) SendEcho(ctx context.Context, data *EchoData) (*EchoData, error) {
	svc.logger.InfoContext(ctx, "Going to echo data", slog.String("message", data.Message))
	return &EchoData{
		Message: data.Message,
	}, nil
}

func NewEchoService(deps EchoServiceDeps) *EchoService {
	return &EchoService{
		logger: deps.RootLogger.WithGroup("app.echo-service"),
	}
}
