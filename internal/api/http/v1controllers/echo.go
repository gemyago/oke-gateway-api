package v1controllers

import (
	"context"
	"net/http"

	"github.com/gemyago/golang-backend-boilerplate/internal/api/http/v1routes/handlers"
	"github.com/gemyago/golang-backend-boilerplate/internal/api/http/v1routes/models"
	"github.com/gemyago/golang-backend-boilerplate/internal/app"
)

type EchoController struct {
	*app.EchoService
}

type sendEchoTransformer struct{}

func (sendEchoTransformer) TransformRequest(
	_ *http.Request,
	echoReq *handlers.EchoSendEchoRequest,
) (*app.EchoData, error) {
	return (*app.EchoData)(echoReq.Payload), nil
}

func (sendEchoTransformer) TransformResponse(
	_ context.Context,
	echoRes *app.EchoData,
) (*models.EchoResponsePayload, error) {
	return (*models.EchoResponsePayload)(echoRes), nil
}

func (c EchoController) SendEcho(b handlers.HandlerBuilder[
	*handlers.EchoSendEchoRequest,
	*models.EchoResponsePayload,
]) http.Handler {
	return b.HandleWith(
		handlers.TransformAction(c.EchoService.SendEcho, sendEchoTransformer{}),
	)
}

var _ handlers.EchoController = (*EchoController)(nil)

func newEchoController(echoService *app.EchoService) *EchoController {
	return &EchoController{EchoService: echoService}
}
