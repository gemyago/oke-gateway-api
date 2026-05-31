package http

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"go.uber.org/dig"

	"github.com/gemyago/oke-gateway-api/internal/diag"
)

type EchoResponse struct {
	RequestHeaders http.Header `json:"requestHeaders"`
	RequestBody    string      `json:"requestBody"`
	RequestMethod  string      `json:"requestMethod"`
	RequestURL     string      `json:"requestURL"`
	Host           string      `json:"host"`
}

type RootHandlerDeps struct {
	dig.In

	RootLogger *slog.Logger

	Mode string `name:"config.httpServer.mode"`
}

const (
	HandlerModeEcho    = "echo"
	HandlerModeStealth = "stealth"
)

func NewRootHandler(deps RootHandlerDeps) http.Handler {
	logger := deps.RootLogger.WithGroup("http-handler")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if deps.Mode == HandlerModeStealth {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		err = json.NewEncoder(w).Encode(EchoResponse{
			RequestHeaders: r.Header,
			RequestBody:    string(body),
			RequestMethod:  r.Method,
			RequestURL:     r.URL.String(),
			Host:           r.Host,
		})
		if err != nil {
			// We can just log at this point, as we've already written a response
			logger.ErrorContext(r.Context(), "failed to encode response", diag.ErrAttr(err))
			return
		}
	})
}
