package middleware

import (
	"net/http"
	"strings"
)

// CORS returns a middleware that allows the configured origins.  If origins
// contains "*" or is empty, every origin is mirrored back (open).  Preflight
// OPTIONS requests are short-circuited with a 204.
func CORS(origins []string) func(http.Handler) http.Handler {
	allow := buildAllowSet(origins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allow(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			} else if len(origins) == 0 || contains(origins, "*") {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func buildAllowSet(origins []string) func(string) bool {
	if len(origins) == 0 || contains(origins, "*") {
		return func(string) bool { return true }
	}
	set := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		set[strings.TrimSpace(o)] = struct{}{}
	}
	return func(o string) bool {
		_, ok := set[o]
		return ok
	}
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}
