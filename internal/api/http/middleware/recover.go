package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// NewRecovererMiddleware creates a middleware that will handle panics
// log them and respond with 500 to client. This should be the last in a chain.
func NewRecovererMiddleware(rootLogger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rvr := recover(); rvr != nil {
					if err, ok := rvr.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						rootLogger.InfoContext(r.Context(), "Request aborted")
						return
					}
					rootLogger.ErrorContext(
						r.Context(),
						"Unhandled panic",
						slog.Any("panic", rvr),
						slog.String("stack", string(debug.Stack())),
					)
					// TODO: Do not write header if already written
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(w, r)
		}

		return http.HandlerFunc(fn)
	}
}
