package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gemyago/oke-gateway-api/internal/api/http/middleware"
	"github.com/gemyago/oke-gateway-api/internal/services"
	sloghttp "github.com/samber/slog-http"
	"go.uber.org/dig"
)

type HTTPServerDeps struct {
	dig.In

	RootLogger *slog.Logger

	// config
	Host              string        `name:"config.httpServer.host"`
	Port              int           `name:"config.httpServer.port"`
	IdleTimeout       time.Duration `name:"config.httpServer.idleTimeout"`
	ReadHeaderTimeout time.Duration `name:"config.httpServer.readHeaderTimeout"`
	ReadTimeout       time.Duration `name:"config.httpServer.readTimeout"`
	WriteTimeout      time.Duration `name:"config.httpServer.writeTimeout"`
	AccessLogsLevel   string        `name:"config.httpServer.accessLogsLevel"`
	Mode              string        `name:"config.httpServer.mode"`

	// handler
	Handler http.Handler

	// services
	*services.ShutdownHooks
}

type HTTPServer struct {
	httpSrv *http.Server
	deps    HTTPServerDeps
	logger  *slog.Logger
}

func (srv *HTTPServer) Start(ctx context.Context) error {
	srv.logger.InfoContext(ctx, "Starting http listener",
		slog.String("addr", srv.httpSrv.Addr),
		slog.String("idleTimeout", srv.deps.IdleTimeout.String()),
		slog.String("readHeaderTimeout", srv.deps.ReadHeaderTimeout.String()),
		slog.String("readTimeout", srv.deps.ReadTimeout.String()),
		slog.String("writeTimeout", srv.deps.WriteTimeout.String()),
		slog.String("accessLogsLevel", srv.deps.AccessLogsLevel),
		slog.String("mode", srv.deps.Mode),
	)
	return srv.httpSrv.ListenAndServe()
}

func buildMiddlewareChain(deps HTTPServerDeps) http.Handler {
	defaultLogLevel := slog.LevelInfo
	clientErrorLevel := slog.LevelWarn
	serverErrorLevel := slog.LevelError

	if deps.AccessLogsLevel != "" {
		if err := defaultLogLevel.UnmarshalText([]byte(deps.AccessLogsLevel)); err != nil {
			panic(fmt.Errorf("failed to unmarshal access logs level: %w", err))
		}
		clientErrorLevel = defaultLogLevel
		serverErrorLevel = defaultLogLevel
	}

	// Router wire-up
	chain := middleware.Chain(
		middleware.NewTracingMiddleware(middleware.NewTracingMiddlewareCfg()),
		sloghttp.NewWithConfig(deps.RootLogger, sloghttp.Config{
			DefaultLevel:     defaultLogLevel,
			ClientErrorLevel: clientErrorLevel,
			ServerErrorLevel: serverErrorLevel,

			WithUserAgent:      true,
			WithRequestID:      false, // We handle it ourselves (tracing middleware)
			WithRequestHeader:  true,
			WithResponseHeader: true,
			WithSpanID:         true,
			WithTraceID:        true,
		}),
		middleware.NewRecovererMiddleware(deps.RootLogger),
	)
	return chain(deps.Handler)
}

// NewHTTPServer constructor factory for general use *http.Server.
func NewHTTPServer(deps HTTPServerDeps) *HTTPServer {
	address := fmt.Sprintf("%s:%d", deps.Host, deps.Port)
	srv := &http.Server{
		Addr:              address,
		IdleTimeout:       deps.IdleTimeout,
		ReadHeaderTimeout: deps.ReadHeaderTimeout,
		ReadTimeout:       deps.ReadTimeout,
		WriteTimeout:      deps.WriteTimeout,
		Handler:           buildMiddlewareChain(deps),
		ErrorLog:          slog.NewLogLogger(deps.RootLogger.Handler(), slog.LevelError),
	}

	deps.ShutdownHooks.Register("http-server", srv.Shutdown)

	return &HTTPServer{
		deps:    deps,
		httpSrv: srv,
		logger:  deps.RootLogger.WithGroup("http-server"),
	}
}
