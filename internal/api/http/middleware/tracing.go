package middleware

import (
	"log/slog"
	"net/http"

	"github.com/gemyago/golang-backend-boilerplate/internal/diag"
	"github.com/gofrs/uuid/v5"
)

type TracingMiddlewareCfg struct {
	generateUUID func() string
}

func NewTracingMiddlewareCfg() *TracingMiddlewareCfg {
	return &TracingMiddlewareCfg{
		generateUUID: func() string {
			return uuid.Must(uuid.NewV4()).String()
		},
	}
}

func NewTracingMiddleware(cfg *TracingMiddlewareCfg) Middleware {
	generateUUID := cfg.generateUUID
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			correlationID := req.Header.Get("x-correlation-id")
			if correlationID == "" {
				correlationID = generateUUID()
			}
			logAttributes := diag.GetLogAttributesFromContext(req.Context())
			logAttributes.CorrelationID = slog.StringValue(correlationID)
			nextCtx := diag.SetLogAttributesToContext(req.Context(), logAttributes)
			next.ServeHTTP(w, req.WithContext(nextCtx))
		})
	}
}
