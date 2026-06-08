package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
	"github.com/gemyago/oke-gateway-api/e2e/internal/controllerproc"
)

func testHTTPStartup(t *testing.T) {
	_, cfg := newLiveHTTPContext(t)
	if cfg.Controller.SkipStart {
		t.Skip("HTTP startup case requires launching the controller; unset OKE_E2E_SKIP_CONTROLLER_START to run it")
	}

	proc := startHTTPController(t, cfg)
	require.NoError(t, proc.Stop())
}

func startHTTPController(t *testing.T, cfg *config.Config) *controllerproc.Process {
	t.Helper()

	proc, err := controllerproc.Start(t, *cfg, nil)
	require.NoError(t, err)

	if proc.Skipped() {
		return proc
	}

	startupCtx, cancel := context.WithTimeout(t.Context(), controllerStartupTimeout)
	defer cancel()

	require.NoError(t, proc.WaitForLog(startupCtx, "Starting controller manager"))

	return proc
}
