package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient(t *testing.T) {
	t.Parallel()

	t.Run("probe decodes the echo response", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewEncoder(w).Encode(EchoResponse{
				RequestHeaders: r.Header,
				RequestBody:    "",
				RequestMethod:  r.Method,
				RequestURL:     r.URL.String(),
				Host:           r.Host,
			}); err != nil {
				t.Errorf("encode echo response: %v", err)
			}
		}))
		t.Cleanup(server.Close)

		publicIP, port := testServerAddress(t, server)
		client, err := NewClient(publicIP, port, nil)
		require.NoError(t, err)

		response, err := client.Probe(t.Context(), "/echo")
		require.NoError(t, err)
		require.NotNil(t, response.Echo)
		assert.True(t, response.IsExpectedEcho("/echo", client.Host()))
	})

	t.Run("wait for echo tolerates transient non-echo responses", func(t *testing.T) {
		t.Parallel()

		var callCount atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if callCount.Add(1) == 1 {
				http.Error(w, "warming up", http.StatusServiceUnavailable)
				return
			}

			if err := json.NewEncoder(w).Encode(EchoResponse{
				RequestHeaders: r.Header,
				RequestBody:    "",
				RequestMethod:  r.Method,
				RequestURL:     r.URL.String(),
				Host:           r.Host,
			}); err != nil {
				t.Errorf("encode echo response: %v", err)
			}
		}))
		t.Cleanup(server.Close)

		publicIP, port := testServerAddress(t, server)
		client, err := NewClient(publicIP, port, nil)
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		t.Cleanup(cancel)

		response, err := WaitForEcho(ctx, client, "/echo", &WaitOptions{PollInterval: time.Millisecond})
		require.NoError(t, err)
		assert.True(t, response.IsExpectedEcho("/echo", client.Host()))
	})

	t.Run("wait for echo gone accepts a non-echo response", func(t *testing.T) {
		t.Parallel()

		var callCount atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if callCount.Add(1) == 1 {
				if err := json.NewEncoder(w).Encode(EchoResponse{
					RequestHeaders: r.Header,
					RequestBody:    "",
					RequestMethod:  r.Method,
					RequestURL:     r.URL.String(),
					Host:           r.Host,
				}); err != nil {
					t.Errorf("encode echo response: %v", err)
				}
				return
			}

			http.NotFound(w, r)
		}))
		t.Cleanup(server.Close)

		publicIP, port := testServerAddress(t, server)
		client, err := NewClient(publicIP, port, nil)
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		t.Cleanup(cancel)

		response, err := WaitForEchoGone(ctx, client, "/echo", &WaitOptions{PollInterval: time.Millisecond})
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, response.StatusCode)
	})
}

func testServerAddress(t *testing.T, server *httptest.Server) (string, int) {
	t.Helper()

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	port, err := strconv.Atoi(serverURL.Port())
	require.NoError(t, err)

	return serverURL.Hostname(), port
}
