package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services"
)

func TestStartServer(t *testing.T) {
	t.Run("returns shutdown errors in noop mode", func(t *testing.T) {
		shutdownHooks := services.NewTestShutdownHooks()
		wantErr := errors.New("shutdown failed")
		shutdownHooks.Register("failing-hook", func(context.Context) error {
			return wantErr
		})

		err := startServer(startServerParams{
			ShutdownHooks: shutdownHooks,
			RootLogger:    diag.RootTestLogger(),
			noop:          true,
		})

		require.ErrorIs(t, err, wantErr)
	})
}
