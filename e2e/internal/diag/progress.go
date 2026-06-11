//go:build !release

package diag

import (
	"context"
	"log/slog"
	"time"
)

const DefaultWaitProgressLogInterval = 15 * time.Second

type WaitProgressLogger struct {
	description string
	interval    time.Duration
	logger      *slog.Logger
	startedAt   time.Time
	lastLogAt   time.Time
}

func NewWaitProgressLogger(
	logger *slog.Logger,
	description string,
	interval time.Duration,
) *WaitProgressLogger {
	if logger == nil {
		logger = RootTestLogger()
	}

	if interval <= 0 {
		interval = DefaultWaitProgressLogInterval
	}

	now := time.Now()

	return &WaitProgressLogger{
		description: description,
		interval:    interval,
		logger:      logger,
		startedAt:   now,
		lastLogAt:   now,
	}
}

func (l *WaitProgressLogger) Log(ctx context.Context, status string, attrs ...slog.Attr) {
	if l == nil || l.logger == nil {
		return
	}

	now := time.Now()
	if now.Sub(l.lastLogAt) < l.interval {
		return
	}

	logAttrs := []slog.Attr{
		slog.String("wait", l.description),
		slog.Duration("elapsed", now.Sub(l.startedAt)),
	}
	if status != "" {
		logAttrs = append(logAttrs, slog.String("status", status))
	}

	logAttrs = append(logAttrs, attrs...)
	l.logger.InfoContext(ctx, "Wait still in progress", slogAttrsToAny(logAttrs)...)
	l.lastLogAt = now
}

func slogAttrsToAny(attrs []slog.Attr) []any {
	values := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		values = append(values, attr)
	}

	return values
}
