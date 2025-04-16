package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestChain(t *testing.T) {
	t.Run("wrap handler with middleware", func(t *testing.T) {
		calls := []string{}

		req := httptest.NewRequest(http.MethodGet, "/something", http.NoBody)
		res := httptest.NewRecorder()

		h := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			calls = append(calls, "handler")
		})
		makeTestMw := func(name string) Middleware {
			return func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls = append(calls, name)
					next.ServeHTTP(w, r)
				})
			}
		}
		wrapped := Chain(makeTestMw("mw1"), makeTestMw("mw2"), makeTestMw("mw3"))(h)
		wrapped.ServeHTTP(res, req)
		assert.Equal(t, []string{
			"mw1", "mw2", "mw3", "handler",
		}, calls)
	})
}
