package main

import (
	"context"
	"errors"
	"log/slog"
	"os/signal"
	"time"

	"github.com/gemyago/golang-backend-boilerplate/internal/api/http"
	"github.com/gemyago/golang-backend-boilerplate/internal/api/http/server"
	"github.com/gemyago/golang-backend-boilerplate/internal/diag"
	"github.com/gemyago/golang-backend-boilerplate/internal/services"
	"github.com/spf13/cobra"
	"go.uber.org/dig"
	"golang.org/x/sys/unix"
)

type startServerParams struct {
	dig.In `ignore-unexported:"true"`

	RootLogger *slog.Logger

	HTTPServer *server.HTTPServer

	*services.ShutdownHooks

	noop bool
}

func startServer(params startServerParams) error {
	rootLogger := params.RootLogger
	httpServer := params.HTTPServer
	rootCtx := context.Background()

	shutdown := func() error {
		rootLogger.InfoContext(rootCtx, "Trying to shut down gracefully")
		ts := time.Now()

		err := params.ShutdownHooks.PerformShutdown(rootCtx)
		if err != nil {
			rootLogger.ErrorContext(rootCtx, "Failed to shut down gracefully", diag.ErrAttr(err))
		}

		rootLogger.InfoContext(rootCtx, "Service stopped",
			slog.Duration("duration", time.Since(ts)),
		)
		return err
	}

	signalCtx, cancel := signal.NotifyContext(rootCtx, unix.SIGINT, unix.SIGTERM)
	defer cancel()

	const startedComponents = 2
	startupErrors := make(chan error, startedComponents)
	go func() {
		if params.noop {
			rootLogger.InfoContext(signalCtx, "NOOP: Starting http server")
			startupErrors <- nil
			return
		}
		startupErrors <- httpServer.Start(signalCtx)
	}()

	var startupErr error
	select {
	case startupErr = <-startupErrors:
		if startupErr != nil {
			rootLogger.ErrorContext(rootCtx, "Server startup failed", "err", startupErr)
		}
	case <-signalCtx.Done(): // coverage-ignore
		// We will attempt to shut down in both cases
		// so doing it once on a next line
	}
	return errors.Join(startupErr, shutdown())
}

func newStartServerCmd(container *dig.Container) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Command to start server",
	}
	noop := false
	cmd.Flags().BoolVar(
		&noop,
		"noop",
		false,
		"Do not start. Just setup deps and exit. Useful for testing if setup is all working.",
	)
	cmd.PreRunE = func(_ *cobra.Command, _ []string) error {
		return errors.Join(
			server.Register(container),
			http.Register(container),
		)
	}
	cmd.RunE = func(_ *cobra.Command, _ []string) error {
		return container.Invoke(func(params startServerParams) error {
			params.noop = noop
			return startServer(params)
		})
	}
	return cmd
}
