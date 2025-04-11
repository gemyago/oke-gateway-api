package middleware

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gemyago/golang-backend-boilerplate/internal/diag"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecover(t *testing.T) {
	rootLogger := diag.RootTestLogger()

	t.Run("should call next", func(t *testing.T) {
		nextCalled := true
		wantNextStatus := 200 + rand.Intn(399)
		wantRes := map[string]interface{}{
			"key1": faker.UUIDHyphenated(),
			"key2": faker.UUIDHyphenated(),
		}
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			nextCalled = true
			w.WriteHeader(wantNextStatus)
			assert.NoError(t, json.NewEncoder(w).Encode(wantRes))
		})
		handler := NewRecovererMiddleware(rootLogger)(next)

		req := httptest.NewRequest(http.MethodPost, "/some-url", http.NoBody)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.True(t, nextCalled)
		assert.Equal(t, wantNextStatus, w.Code)

		var gotRes map[string]interface{}
		require.NoError(t, json.NewDecoder(w.Body).Decode(&gotRes))
		assert.Equal(t, wantRes, gotRes)
	})

	t.Run("should recover from panic", func(t *testing.T) {
		nextCalled := false
		next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			nextCalled = true
			panic("some error")
		})
		handler := NewRecovererMiddleware(rootLogger)(next)

		req := httptest.
			NewRequest(http.MethodPost, "/some-url", http.NoBody)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.True(t, nextCalled)
		assert.Equal(t, 500, w.Code)
		assert.Empty(t, w.Body.Bytes())
	})

	t.Run("ignore aborted request", func(t *testing.T) {
		nextCalled := false
		next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			nextCalled = true
			panic(fmt.Errorf("request aborted: %w", http.ErrAbortHandler))
		})
		handler := NewRecovererMiddleware(rootLogger)(next)

		req := httptest.NewRequest(http.MethodPost, "/some-url", http.NoBody)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.True(t, nextCalled)
		assert.Equal(t, 200, w.Code) // default status code
		assert.Empty(t, w.Body.Bytes())
	})
}
