package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"errors"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockErrorReader simulates an io.Reader that returns an error.
type mockErrorReader struct{}

func (m *mockErrorReader) Read(_ []byte) (int, error) {
	return 0, errors.New("forced read error")
}

// mockResponseWriter simulates an http.ResponseWriter whose Write method fails.
type mockResponseWriter struct {
	headers    http.Header
	statusCode int
	body       *bytes.Buffer
}

func newMockResponseWriter() *mockResponseWriter {
	return &mockResponseWriter{
		headers: make(http.Header),
		body:    new(bytes.Buffer),
	}
}

func (m *mockResponseWriter) Header() http.Header {
	return m.headers
}

func (m *mockResponseWriter) WriteHeader(statusCode int) {
	if m.statusCode != 0 {
		return
	}
	m.statusCode = statusCode
}

// Add back the Write method to satisfy the interface.
func (m *mockResponseWriter) Write([]byte) (int, error) {
	if m.statusCode == 0 {
		m.WriteHeader(http.StatusOK)
	}
	return 0, errors.New("forced write error")
}

func TestRootHandler(t *testing.T) {
	makeMockDeps := func() RootHandlerDeps {
		return RootHandlerDeps{
			RootLogger: diag.RootTestLogger(),
			Mode:       HandlerModeEcho,
		}
	}

	t.Run("should respond with details", func(t *testing.T) {
		requestPath := "/path1/" + faker.Word() + "/path2"

		queryValues := url.Values{}
		queryValues.Set("key1-"+faker.Word(), faker.Word())
		queryValues.Set("key2-"+faker.Word(), faker.Word())
		wantBody := faker.Sentence()
		req := httptest.NewRequest(http.MethodPost, requestPath+"?"+queryValues.Encode(), bytes.NewBufferString(wantBody))
		w := httptest.NewRecorder()
		deps := makeMockDeps()
		rootHandler := NewRootHandler(deps)
		rootHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var got EchoResponse
		err := json.NewDecoder(w.Body).Decode(&got)
		require.NoError(t, err)
		assert.Equal(t, EchoResponse{
			RequestHeaders: req.Header,
			RequestBody:    wantBody,
			RequestMethod:  req.Method,
			RequestURL:     req.URL.String(),
			Host:           req.Host,
		}, got)
	})

	t.Run("just responds with 404 in stealth mode", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		deps := makeMockDeps()
		deps.Mode = HandlerModeStealth
		rootHandler := NewRootHandler(deps)
		rootHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.Equal(t, "", w.Body.String())
	})

	t.Run("should return 500 on body read error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", &mockErrorReader{})
		w := httptest.NewRecorder()
		deps := makeMockDeps()
		rootHandler := NewRootHandler(deps)
		rootHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		assert.Contains(t, w.Body.String(), "forced read error")
	})

	t.Run("should handle response encode error", func(t *testing.T) {
		mockWriter := newMockResponseWriter()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		deps := makeMockDeps()
		rootHandler := NewRootHandler(deps)

		rootHandler.ServeHTTP(mockWriter, req)

		assert.Equal(t, http.StatusOK, mockWriter.statusCode, "WriteHeader should be called with OK before write error")
	})
}
