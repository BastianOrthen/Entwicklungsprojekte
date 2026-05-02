package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover catches panics in downstream handlers, logs them with stack trace,
// and returns a 500 response so that one bad handler cannot crash the server.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"path", r.URL.Path,
						"method", r.Method,
						"panic", rec,
						"stack", string(debug.Stack()),
					)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
