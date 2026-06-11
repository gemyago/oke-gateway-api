package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jaswdr/faker/v2"
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

	t.Run("probe request overrides host and forwards headers", func(t *testing.T) {
		t.Parallel()

		fake := faker.New()
		requestHost := "route-a-" + fake.UUID().V4() + ".example.test"
		headerValue := "host-override-" + fake.UUID().V4()

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

		response, err := client.ProbeRequest(t.Context(), "/echo", &RequestOptions{
			Host: requestHost,
			Headers: http.Header{
				"X-Test-Case": []string{headerValue},
			},
		})
		require.NoError(t, err)
		require.NotNil(t, response.Echo)
		assert.Equal(t, requestHost, response.Echo.Host)
		assert.Equal(t, []string{headerValue}, response.Echo.RequestHeaders.Values("X-Test-Case"))
	})

	t.Run("probe captures peer certificates over https", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		rootCAs := x509.NewCertPool()
		rootCAs.AddCert(server.Certificate())
		client, err := NewClient(publicIP, port, &ClientOptions{
			Scheme: "https",
			HTTPClient: &http.Client{
				Timeout: time.Second,
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{RootCAs: rootCAs},
				},
			},
		})
		require.NoError(t, err)

		response, err := client.Probe(t.Context(), "/echo")
		require.NoError(t, err)
		require.NotEmpty(t, response.TLSPeerCertificates)
		assert.Equal(t, server.Certificate().RawSubject, response.TLSPeerCertificates[0].RawSubject)
		assert.NotEmpty(t, response.TLSVerifiedChains)
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

	t.Run("wait for echo request uses the overridden host", func(t *testing.T) {
		t.Parallel()

		fake := faker.New()
		requestHost := "route-b-" + fake.UUID().V4() + ".example.test"

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

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		t.Cleanup(cancel)

		response, err := WaitForEchoRequest(ctx, client, "/echo", &RequestOptions{
			Host: requestHost,
		}, &WaitOptions{PollInterval: time.Millisecond})
		require.NoError(t, err)
		assert.True(t, response.IsExpectedEcho("/echo", requestHost))
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

	t.Run("wait for response supports custom body matchers", func(t *testing.T) {
		t.Parallel()

		fake := faker.New()
		responseBody := "backend-a-" + fake.UUID().V4()

		var callCount atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if callCount.Add(1) == 1 {
				http.Error(w, "warming up", http.StatusServiceUnavailable)
				return
			}

			if _, err := w.Write([]byte(responseBody)); err != nil {
				t.Errorf("write response: %v", err)
			}
		}))
		t.Cleanup(server.Close)

		publicIP, port := testServerAddress(t, server)
		client, err := NewClient(publicIP, port, nil)
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		t.Cleanup(cancel)

		response, err := WaitForResponse(
			ctx,
			client,
			"/",
			nil,
			&WaitOptions{PollInterval: time.Millisecond},
			"wait for randomized backend body",
			func(response *Response) (bool, string) {
				if response.StatusCode != http.StatusOK {
					return false, "status not ready"
				}

				if response.BodyString() == responseBody {
					return true, ""
				}

				return false, "unexpected body"
			},
		)
		require.NoError(t, err)
		assert.Equal(t, responseBody, response.BodyString())
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
