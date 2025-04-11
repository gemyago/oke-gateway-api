// ShutdownHooks are used to implement a graceful shutdown
// of the application. This may include closing database connections, flushing pending
// events to the queue, shutting down the http server, etc.

package services

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"go.uber.org/dig"
	"golang.org/x/sync/errgroup"
)

type shutdownHook struct {
	name       string
	shutdownFn func(ctx context.Context) error
}

type ShutdownHooks struct {
	logger *slog.Logger
	hooks  []shutdownHook
	deps   ShutdownHooksRegistryDeps
}

// HasHook checks if a shutdown hook with the given name is registered.
// Typical usage is in tests and must be carefully considered for production scenarios.
func (h *ShutdownHooks) HasHook(name string, method any) bool {
	for _, hook := range h.hooks {
		if hook.name == name {
			return reflect.ValueOf(hook.shutdownFn).Pointer() == reflect.ValueOf(method).Pointer()
		}
	}
	return false
}

func (h *ShutdownHooks) Register(name string, shutdown func(ctx context.Context) error) {
	h.hooks = append(h.hooks, shutdownHook{name: name, shutdownFn: shutdown})
}

func (h *ShutdownHooks) RegisterNoCtx(name string, shutdown func() error) {
	h.Register(name, func(_ context.Context) error {
		return shutdown()
	})
}

func (h *ShutdownHooks) PerformShutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, h.deps.GracefulShutdownTimeout)
	defer cancel()

	errGrp := errgroup.Group{}
	for _, hook := range h.hooks {
		errGrp.Go(func() error {
			hookName := hook.name
			h.logger.InfoContext(ctx, fmt.Sprintf("Shutting down %s", hookName))
			if err := hook.shutdownFn(ctx); err != nil {
				return fmt.Errorf("failed to perform shutdown hook %s: %w", hookName, err)
			}
			return nil
		})
	}

	done := make(chan error)
	go func() {
		done <- errGrp.Wait()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type ShutdownHooksRegistryDeps struct {
	dig.In

	RootLogger *slog.Logger

	// config
	GracefulShutdownTimeout time.Duration `name:"config.gracefulShutdownTimeout"`
}

func NewShutdownHooks(deps ShutdownHooksRegistryDeps) *ShutdownHooks {
	return &ShutdownHooks{
		logger: deps.RootLogger.WithGroup("shutdown"),
		deps:   deps,
	}
}
