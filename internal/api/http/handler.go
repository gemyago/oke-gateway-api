package http

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/gemyago/oke-gateway-api/internal/diag"
	"go.uber.org/dig"
)

type EchoResponse struct {
	RequestHeaders http.Header `json:"requestHeaders"`
	RequestBody    string      `json:"requestBody"`
	RequestMethod  string      `json:"requestMethod"`
	RequestURL     string      `json:"requestURL"`
}

type RootHandlerDeps struct {
	dig.In

	RootLogger *slog.Logger
}

func NewRootHandler(deps RootHandlerDeps) http.Handler {
	logger := deps.RootLogger.WithGroup("http-handler")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		err = json.NewEncoder(w).Encode(EchoResponse{
			RequestHeaders: r.Header,
			RequestBody:    string(body),
			RequestMethod:  r.Method,
			RequestURL:     r.URL.String(),
		})
		if err != nil {
			// We can just log at this point, as we've already written a response
			logger.Error("failed to encode response", diag.ErrAttr(err))
			return
		}
	})
}
