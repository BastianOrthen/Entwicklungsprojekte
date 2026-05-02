// Package health provides standardised /healthz (liveness) and /readyz
// (readiness) HTTP handlers for SAW services.  Liveness is always green if
// the process is up.  Readiness can be gated on registered checks (database
// reachable, NATS connected, upstream gRPC healthy, …).
package health

import (
	"encoding/json"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// CheckFunc returns nil when the dependency is healthy, otherwise an error
// describing the failure.  Implementations must be cheap and non-blocking.
type CheckFunc func() error

// Service holds the readiness checks for a single process.
type Service struct {
	name    string
	version string
	started time.Time

	mu     sync.RWMutex
	checks map[string]CheckFunc
}

// New returns a new health service for the given component.
func New(name, version string) *Service {
	return &Service{
		name:    name,
		version: version,
		started: time.Now(),
		checks:  map[string]CheckFunc{},
	}
}

// Register adds a named readiness check.  Subsequent registrations under the
// same name overwrite the previous one (handy in tests).
func (s *Service) Register(name string, fn CheckFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks[name] = fn
}

// LivenessHandler reports HTTP 200 as long as the process is responsive.
func (s *Service) LivenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": s.name,
			"version": s.version,
			"uptime":  time.Since(s.started).String(),
			"go":      runtime.Version(),
		})
	}
}

// ReadinessHandler runs every registered check.  Returns 200 with all results
// when every check passed, otherwise 503.
func (s *Service) ReadinessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()

		results := make(map[string]string, len(s.checks))
		ok := true
		for name, fn := range s.checks {
			if err := fn(); err != nil {
				results[name] = "fail: " + err.Error()
				ok = false
			} else {
				results[name] = "ok"
			}
		}
		status := http.StatusOK
		if !ok {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{
			"status":  ternary(ok, "ready", "not_ready"),
			"service": s.name,
			"checks":  results,
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func ternary[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}
