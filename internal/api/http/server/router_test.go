package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
)

func TestMuxRouterAdapter(t *testing.T) {
	t.Run("should handle routes and read path values", func(t *testing.T) {
		wantPathParam := faker.Word()
		req := httptest.NewRequest(
			http.MethodGet,
			fmt.Sprintf("/resources/%s/value", wantPathParam),
			http.NoBody,
		)

		adapter := (*HTTPRouter)(http.NewServeMux())
		handlerInvoked := false
		adapter.HandleRoute(
			http.MethodGet,
			"/resources/{param}/value",
			http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				gotPathParam := adapter.PathValue(r, "param")
				assert.Equal(t, wantPathParam, gotPathParam)
				handlerInvoked = true
			}))
		adapter.ServeHTTP(httptest.NewRecorder(), req)
		assert.True(t, handlerInvoked)
	})
}
