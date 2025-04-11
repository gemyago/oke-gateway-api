package v1controllers

import (
	"context"
	"net/http"

	"github.com/gemyago/golang-backend-boilerplate/internal/api/http/v1routes/handlers"
	"github.com/gemyago/golang-backend-boilerplate/internal/api/http/v1routes/models"
)

type HealthController struct{}

func (HealthController) HealthCheck(
	b handlers.NoParamsHandlerBuilder[*models.HealthResponsePayload],
) http.Handler {
	return b.HandleWith(func(_ context.Context) (*models.HealthResponsePayload, error) {
		return &models.HealthResponsePayload{
			Status: models.HealthResponsePayloadStatusOK,
		}, nil
	})
}

var _ handlers.HealthController = (*HealthController)(nil)
