package server

import (
	"math/rand/v2"
	"net/http"
	"syscall"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/gemyago/oke-gateway-api/internal/services"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPServer(t *testing.T) {
	t.Run("Startup/Shutdown", func(t *testing.T) {
		t.Run("should start and stop the server", func(t *testing.T) {
			hooks := services.NewTestShutdownHooks()
			srv := NewHTTPServer(HTTPServerDeps{
				RootLogger:    diag.RootTestLogger(),
				Host:          "localhost",
				Port:          50000 + rand.IntN(15000),
				ShutdownHooks: hooks,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
			})
			assert.True(t, hooks.HasHook("http-server", srv.httpSrv.Shutdown))

			stopCh := make(chan error)
			startedSignal := make(chan struct{})
			ctx := t.Context()
			go func() {
				close(startedSignal)
				stopCh <- srv.Start(ctx)
			}()
			<-startedSignal
			res, err := http.Get("http://" + srv.httpSrv.Addr)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, res.StatusCode)

			require.NoError(t, srv.httpSrv.Shutdown(ctx))

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
