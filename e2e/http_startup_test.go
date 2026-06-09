package e2e

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
	"github.com/gemyago/oke-gateway-api/e2e/internal/controllerproc"
)

func testHTTPStartup(t *testing.T) {
	logger := startTestLogger(t)
	_, cfg := newLiveHTTPContext(t)
	logger.Info("Loaded live HTTP startup configuration",
		slog.String("kubeContext", cfg.Kubernetes.Context),
		slog.String("controllerBin", cfg.Controller.BinPath),
		slog.Bool("skipControllerStart", cfg.Controller.SkipStart),
	)
	if cfg.Controller.SkipStart {
		t.Skip("HTTP startup case requires launching the controller; unset OKE_E2E_SKIP_CONTROLLER_START to run it")
	}

	proc := startHTTPController(t, cfg, logger)
	logTestProgress(
		t,
		logger,
		"Controller started for startup validation",
		slog.Int("pid", proc.PID()),
	)
	require.NoError(t, proc.Stop())
	logTestProgress(t, logger, "Controller stopped cleanly after startup validation")
}

func startHTTPController(t *testing.T, cfg *config.Config, logger *slog.Logger) *controllerproc.Process {
	t.Helper()

	logTestProgress(t, logger, "Starting controller process")
	proc, err := controllerproc.Start(newSlogTestLogSink(t, logger), *cfg, nil)
	require.NoError(t, err)

	if proc.Skipped() {
		logTestProgress(t, logger, "Controller startup skipped by configuration")
		return proc
	}

	startupCtx, cancel := context.WithTimeout(t.Context(), controllerStartupTimeout)
	defer cancel()

	logTestProgressContext(
		startupCtx,
		t,
		logger,
		"Waiting for controller startup log",
		slog.String("fragment", "Starting controller manager"),
	)
	require.NoError(t, proc.WaitForLog(startupCtx, "Starting controller manager"))
	logTestProgress(t, logger, "Observed controller startup log")

	return proc
}
