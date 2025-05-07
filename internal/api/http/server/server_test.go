package server

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPServer(t *testing.T) {
	t.Run("Startup/Shutdown", func(t *testing.T) {
		t.Run("should start and stop the server", func(t *testing.T) {
			hooks := services.NewTestShutdownHooks()
			port := 50000 + rand.IntN(15000)
			addr := fmt.Sprintf("localhost:%d", port)
			listeningSignal := make(chan struct{})

			srv := NewHTTPServer(HTTPServerDeps{
				RootLogger:    diag.RootTestLogger(),
				Host:          "localhost",
				Port:          port,
				ShutdownHooks: hooks,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
				listeningSignal: listeningSignal,
			})
			assert.True(t, hooks.HasHook("http-server", srv.httpSrv.Shutdown))

			stopCh := make(chan error, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()

			go func() {
				stopCh <- srv.Start(ctx)
			}()

			select {
			case <-listeningSignal:
			case err := <-stopCh:
				t.Fatalf("server failed to start: %v", err)
			case <-ctx.Done():
				t.Fatalf("server failed to signal readiness in time: %v", ctx.Err())
			}

			res, err := http.Get("http://" + addr)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, res.StatusCode)

			require.NoError(t, srv.httpSrv.Shutdown(ctx), "httpSrv.Shutdown failed")

			select {
			case err = <-stopCh:
				require.NoError(t, err, "srv.Start returned an unexpected error on shutdown")
			case <-ctx.Done():
				t.Fatalf("server failed to shutdown in time: %v", ctx.Err())
			}

			_, err = http.Get("http://" + addr)
			require.Error(t, err, "expected connection error after shutdown")

			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				t.Errorf("expected connection refused, but got timeout error: %v", err)
			}

			_, err = http.Get("http://" + srv.httpSrv.Addr)
			require.Error(t, err)
			assert.ErrorIs(t, err, syscall.ECONNREFUSED)
		})
	})
}

func TestBuildMiddlewareChain(t *testing.T) {
	t.Run("should use default log levels when AccessLogsLevel is empty", func(t *testing.T) {
		deps := HTTPServerDeps{
			RootLogger: diag.RootTestLogger(),
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
		}

		// We can't easily inspect the log levels set inside the sloghttp middleware directly.
		// However, we can check that the function doesn't panic when AccessLogsLevel is empty.
		// This indirectly tests the default path.
		assert.NotPanics(t, func() {
			_ = buildMiddlewareChain(deps)
		})
	})

	t.Run("should use custom log levels when AccessLogsLevel is valid", func(t *testing.T) {
		deps := HTTPServerDeps{
			RootLogger:      diag.RootTestLogger(),
			AccessLogsLevel: "debug", // Using a valid level
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
		}

		// Similar to the default case, we verify it doesn't panic,
		// which covers lines 63, 66, and 67 successfully processing the custom level.
		assert.NotPanics(t, func() {
			_ = buildMiddlewareChain(deps)
		})
	})

	t.Run("should panic when AccessLogsLevel is invalid", func(t *testing.T) {
		deps := HTTPServerDeps{
			RootLogger:      diag.RootTestLogger(),
			AccessLogsLevel: "invalid-level", // An invalid level
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
		}

		// This directly tests the panic condition on line 64.
		require.Panics(t, func() {
			_ = buildMiddlewareChain(deps)
		})
	})
}
