package observability

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the canonical SAW Prometheus metrics.  One instance per
// process; pass it to handlers/middleware that need to record measurements.
type Metrics struct {
	registry *prometheus.Registry

	// HTTP
	HTTPRequests *prometheus.CounterVec   // labels: method, path, status
	HTTPDuration *prometheus.HistogramVec // labels: method, path

	// WebSocket
	WSConnections prometheus.Gauge
	WSMessages    *prometheus.CounterVec // labels: direction (in/out), type

	// Sensor ingest (gRPC)
	SensorIngest *prometheus.CounterVec // labels: kind (localization/bearing/detection)

	// Alarms
	AlarmEvents *prometheus.CounterVec // labels: rule_type, severity

	// REMP / fanout
	RempPublished *prometheus.CounterVec // labels: kind (full/overlay), trigger
	FanoutClients prometheus.Gauge       // current TCP fanout subscribers
}

// NewMetrics creates and registers the canonical metric set under a service
// namespace (e.g. "saw_api", "saw_bridge", "saw_mht").
func NewMetrics(namespace string) *Metrics {
	reg := prometheus.NewRegistry()
	// Re-register Go + process collectors so /metrics is useful out of the box.
	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	m := &Metrics{registry: reg}

	m.HTTPRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "http", Name: "requests_total",
		Help: "Number of HTTP requests handled, partitioned by method, path and status code.",
	}, []string{"method", "path", "status"})

	m.HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "http", Name: "request_duration_seconds",
		Help:    "HTTP request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	m.WSConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "ws", Name: "connections",
		Help: "Current number of open WebSocket client connections.",
	})

	m.WSMessages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ws", Name: "messages_total",
		Help: "WebSocket messages, partitioned by direction and type.",
	}, []string{"direction", "type"})

	m.SensorIngest = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "ingest", Name: "messages_total",
		Help: "Sensor messages received over gRPC, partitioned by kind.",
	}, []string{"kind"})

	m.AlarmEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "alarm", Name: "events_total",
		Help: "Alarm events fired, partitioned by rule type and severity.",
	}, []string{"rule_type", "severity"})

	m.RempPublished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "remp", Name: "published_total",
		Help: "REMP snapshots successfully published to subscribers.",
	}, []string{"kind", "trigger"})

	m.FanoutClients = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "fanout", Name: "clients",
		Help: "Current number of TCP fanout subscribers.",
	})

	reg.MustRegister(
		m.HTTPRequests, m.HTTPDuration,
		m.WSConnections, m.WSMessages,
		m.SensorIngest,
		m.AlarmEvents,
		m.RempPublished, m.FanoutClients,
	)
	return m
}

// Registry exposes the underlying registry for tests / advanced use.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler returns the http.Handler for the /metrics endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

// HTTPMiddleware records request count and latency.  Wrap your router with it.
//
//	mux := http.NewServeMux()
//	handler := metrics.HTTPMiddleware(mux)
func (m *Metrics) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.HTTPRequests.WithLabelValues(r.Method, r.URL.Path, strconv.Itoa(rec.status)).Inc()
		m.HTTPDuration.WithLabelValues(r.Method, r.URL.Path).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Hijack passes through so WebSocket upgrades keep working through this
// middleware (gorilla/websocket needs http.Hijacker).
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hj.Hijack()
}

func (r *statusRecorder) Flush() {
	if fl, ok := r.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}
