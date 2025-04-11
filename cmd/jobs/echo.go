package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/gemyago/golang-backend-boilerplate/internal/api/http"
	"github.com/gemyago/golang-backend-boilerplate/internal/api/http/server"
	"github.com/gemyago/golang-backend-boilerplate/internal/app"
	"github.com/spf13/cobra"
	"go.uber.org/dig"
)

// This is an example of minimalistic job. In real world it would do something more useful.

type echoJobParams struct {
	dig.In `ignore-unexported:"true"`

	RootLogger *slog.Logger

	*app.EchoService

	noop bool
}

func runEchoJob(ctx context.Context, params echoJobParams) error {
	rootLogger := params.RootLogger
	echoService := params.EchoService
	res, err := echoService.SendEcho(ctx, &app.EchoData{Message: "Hello, World!"})
	if err != nil {
		return fmt.Errorf("failed to send echo: %w", err)
	}
	rootLogger.InfoContext(ctx, "Echo succeeded", slog.String("response", res.Message))
	return nil
}

func newStartServerCmd(container *dig.Container) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "echo",
		Short: "Command run echo job",
	}
	noop := false
	cmd.Flags().BoolVar(
		&noop,
		"noop",
		false,
		"Run in noop mode. Useful for testing if setup is all working.",
	)
	cmd.PreRunE = func(_ *cobra.Command, _ []string) error {
		return errors.Join(
			server.Register(container),
			http.Register(container),
		)
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return container.Invoke(func(params echoJobParams) error {
			params.noop = noop
			return runEchoJob(cmd.Context(), params)
		})
	}
	return cmd
}
