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
