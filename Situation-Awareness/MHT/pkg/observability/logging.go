// Package observability provides shared logging, metrics and tracing setup
// for all SAW services.  Following MDS conventions, this package is the only
// place that configures structured logging (slog), the Prometheus registry
// and (in future) the OpenTelemetry tracer.
//
// Usage from a service entry point:
//
//	observability.SetupLogger(observability.LogConfig{Level: cfg.LogLevel, Format: "text"})
//	metrics := observability.NewMetrics("saw_api")
//	mux.Handle("/metrics", metrics.Handler())
package observability

import (
	"log/slog"
	"os"
	"strings"
)

// LogConfig configures the global slog logger.
type LogConfig struct {
	// Level is one of "debug", "info", "warn", "error".  Defaults to "info".
	Level string
	// Format is "json" or "text".  Defaults to "text".
	Format string
	// Service is added as a base attribute to every record (e.g. "saw-api").
	Service string
	// Version is added as a base attribute (e.g. "0.1.0").
	Version string
}

// SetupLogger installs a slog.Logger as the default.  All subsequent
// slog.Info/Warn/Error calls in the process will use it.
func SetupLogger(c LogConfig) *slog.Logger {
	level := parseLevel(c.Level)

	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if strings.EqualFold(c.Format, "json") {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}

	attrs := []slog.Attr{}
	if c.Service != "" {
		attrs = append(attrs, slog.String("service", c.Service))
	}
	if c.Version != "" {
		attrs = append(attrs, slog.String("version", c.Version))
	}
	if len(attrs) > 0 {
		h = h.WithAttrs(attrs)
	}

	logger := slog.New(h)
	slog.SetDefault(logger)
	return logger
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
