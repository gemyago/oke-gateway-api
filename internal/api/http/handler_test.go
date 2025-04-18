package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootHandler(t *testing.T) {
	makeMockDeps := func() RootHandlerDeps {
		return RootHandlerDeps{
			RootLogger: diag.RootTestLogger(),
			Mode:       "echo",
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
		deps.Mode = "stealth"
		rootHandler := NewRootHandler(deps)
		rootHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.Equal(t, "", w.Body.String())
	})
}
