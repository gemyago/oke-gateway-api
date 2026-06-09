//go:build !release

package e2e

import (
	"fmt"
	"log/slog"
	"testing"

	"github.com/gemyago/oke-gateway-api/e2e/internal/diag"
)

func rootTestLogger() *slog.Logger {
	return diag.RootTestLogger()
}

func startTestLogger(t *testing.T) *slog.Logger {
	t.Helper()

	logger := rootTestLogger().With(slog.String("test", t.Name()))
	logger.Info("Starting test - " + t.Name())
	t.Cleanup(func() {
		logger.Info("Finished test - " + t.Name())
	})

	return logger
}

type slogTestLogSink struct {
	t      *testing.T
	logger *slog.Logger
}

func newSlogTestLogSink(t *testing.T, logger *slog.Logger) *slogTestLogSink {
	t.Helper()

	if logger == nil {
		logger = startTestLogger(t)
	}

	return &slogTestLogSink{
		t:      t,
		logger: logger,
	}
}

func (s *slogTestLogSink) Helper() {
	s.t.Helper()
}

func (s *slogTestLogSink) Cleanup(fn func()) {
	s.t.Cleanup(fn)
}

func (s *slogTestLogSink) Logf(format string, args ...any) {
	s.t.Helper()
	s.logger.Info(fmt.Sprintf(format, args...))
}

func (s *slogTestLogSink) Errorf(format string, args ...any) {
	s.t.Helper()
	s.logger.Error(fmt.Sprintf(format, args...))
}
