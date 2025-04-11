package http

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gemyago/golang-backend-boilerplate/internal/api/http/server"
	"github.com/gemyago/golang-backend-boilerplate/internal/api/http/v1controllers"
	"github.com/gemyago/golang-backend-boilerplate/internal/api/http/v1routes/handlers"
	"github.com/gemyago/golang-backend-boilerplate/internal/di"
	"go.uber.org/dig"
)

// Use apigen to generate v1routes
//go:generate go run github.com/gemyago/apigen ./v1routes.yaml ./v1routes

type V1RoutesDeps struct {
	dig.In

	RootLogger *slog.Logger

	*v1controllers.HealthController
	*v1controllers.EchoController
}

func NewRootHandler(deps V1RoutesDeps) http.Handler { // coverage-ignore // Little value in testing wireup code.
	logger := deps.RootLogger.WithGroup("http")

	rootHandler := handlers.NewRootHandler(
		(*server.HTTPRouter)(http.NewServeMux()),
		handlers.WithLogger(logger),
	)
	rootHandler.RegisterHealthRoutes(deps.HealthController)
	rootHandler.RegisterEchoRoutes(deps.EchoController)

	return rootHandler
}

func Register(container *dig.Container) error {
	return errors.Join(
		v1controllers.Register(container),
		di.ProvideAll(container,
			NewRootHandler,
		),
	)
}
