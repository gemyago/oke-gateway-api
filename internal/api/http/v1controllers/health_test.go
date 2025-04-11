package v1controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gemyago/golang-backend-boilerplate/internal/api/http/server"
	"github.com/gemyago/golang-backend-boilerplate/internal/api/http/v1routes/handlers"
	"github.com/stretchr/testify/assert"
)

func TestHealthCheck(t *testing.T) {
	type mockDeps struct {
		HealthController handlers.HealthController
	}
	makeDeps := func() mockDeps {
		deps := mockDeps{
			HealthController: HealthController{},
		}
		return deps
	}

	t.Run("GET /health", func(t *testing.T) {
		t.Run("should respond with OK", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/health", http.NoBody)
			w := httptest.NewRecorder()
			deps := makeDeps()
			rootHandler := handlers.
				NewRootHandler((*server.HTTPRouter)(http.NewServeMux())).
				RegisterHealthRoutes(deps.HealthController)
			rootHandler.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.JSONEq(t, `{"status": "OK"}`, w.Body.String())
		})
	})
}
