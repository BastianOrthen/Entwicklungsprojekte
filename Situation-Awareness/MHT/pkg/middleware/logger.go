package middleware

import (
	"bufio"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// RequestLogger emits one slog record per HTTP request, level INFO for 2xx/3xx,
// WARN for 4xx, ERROR for 5xx.  Skips /metrics, /healthz and /readyz to avoid
// log spam from probes.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isProbe(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			dur := time.Since(start)

			level := slog.LevelInfo
			switch {
			case rec.status >= 500:
				level = slog.LevelError
			case rec.status >= 400:
				level = slog.LevelWarn
			}
			logger.Log(r.Context(), level, "http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", dur.Milliseconds(),
				"remote", r.RemoteAddr,
			)
		})
	}
}

func isProbe(p string) bool {
	switch p {
	case "/metrics", "/healthz", "/readyz", "/health":
		return true
	default:
		return false
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack passes through to the underlying ResponseWriter so that protocols
// requiring Hijacker (most notably WebSocket upgrades via gorilla/websocket)
// keep working when this middleware is in the chain.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hj.Hijack()
}

// Flush passes through to the underlying ResponseWriter when it supports
// flushing (SSE, chunked streaming).
func (r *statusRecorder) Flush() {
	if fl, ok := r.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}
