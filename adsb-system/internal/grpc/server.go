// Package grpc provides HTTP-based streaming (SSE) and ingest endpoints for aircraft data.
// Despite the package name, it currently uses HTTP/SSE rather than gRPC for broader compatibility.
// It implements a broadcaster that maintains connections to multiple clients and streams
// aircraft position updates in real-time.
package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/basti/adsb-system/internal/adsb"
	dbpkg "github.com/basti/adsb-system/internal/db"
)

// Broadcaster manages real-time distribution of aircraft updates to multiple subscribers.
// It uses an in-memory channel-based system for low-latency streaming.
// Thread-safe and suitable for up to hundreds of concurrent clients.
type Broadcaster struct {
	mu       sync.Mutex
	clients  map[chan adsb.Aircraft]struct{}
	aircraft map[string]adsb.Aircraft // Current aircraft state for HTMX queries
	db       *sql.DB
}

// NewBroadcaster creates a new broadcaster instance.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		clients:  make(map[chan adsb.Aircraft]struct{}),
		aircraft: make(map[string]adsb.Aircraft),
	}
}

// SetDB attaches a database connection used for persistence.
// Safe to call before or after StartHTTP.
func (b *Broadcaster) SetDB(db *sql.DB) {
	b.mu.Lock()
	b.db = db
	b.mu.Unlock()
}

// Subscribe returns a channel that receives aircraft updates until ctx is done.
// Each subscriber gets their own dedicated channel to prevent blocking other clients.
func (b *Broadcaster) Subscribe(ctx context.Context) <-chan adsb.Aircraft {
	ch := make(chan adsb.Aircraft, 16)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.clients, ch)
		close(ch)
		b.mu.Unlock()
	}()
	return ch
}

// Broadcast sends an aircraft update to all connected subscribers.
// Uses non-blocking sends; slow clients are skipped to prevent head-of-line blocking.
// Also stores the aircraft in the in-memory cache for HTMX queries.
func (b *Broadcaster) Broadcast(a adsb.Aircraft) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Store in cache for HTMX endpoints
	b.aircraft[a.ICAO] = a
	// Broadcast to subscribers
	for ch := range b.clients {
		select {
		case ch <- a:
		default:
		}
	}
}

// StartHTTP starts the HTTP server with SSE streaming (/stream) and ingest (/ingest) endpoints.
// The server listens on the specified address and handles CORS for browser-based clients.
// Returns immediately; the server runs in background goroutines.
func (b *Broadcaster) StartHTTP(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", b.handleStream)
	mux.HandleFunc("/ingest", b.handleIngest)
	mux.HandleFunc("/healthz", handleHealth)
	mux.HandleFunc("/debug", b.handleDebug)
	// HTMX endpoints
	mux.HandleFunc("/api/aircraft", b.handleAircraftJSON)
	mux.HandleFunc("/api/aircraft-rows", b.handleAircraftRows)
	mux.HandleFunc("/api/history", b.handleHistoryJSON)
	mux.HandleFunc("/api/history/latest", b.handleHistoryLatestJSON)

	return b.serveHTTP(addr, mux)
}

// handleHistoryLatestJSON returns the latest ADS-B report per ICAO from Postgres as JSON.
// This is designed to keep payloads small for large time windows.
// Query params:
//   - source: antenna|simulator|internet
//   - from: RFC3339
//   - to: RFC3339
//   - fields: comma-separated; interpreted as filter criteria (field must be present).
//     Examples: callsign,squawk,origin_country
//   - limit: optional cap on number of aircraft returned
func (b *Broadcaster) handleHistoryLatestJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	b.mu.Lock()
	db := b.db
	b.mu.Unlock()
	if db == nil {
		http.Error(w, `{"error":"postgres not configured"}`, http.StatusServiceUnavailable)
		return
	}

	fromStr := strings.TrimSpace(r.URL.Query().Get("from"))
	toStr := strings.TrimSpace(r.URL.Query().Get("to"))
	if fromStr == "" || toStr == "" {
		http.Error(w, `{"error":"from and to are required (RFC3339)"}`, http.StatusBadRequest)
		return
	}
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		http.Error(w, `{"error":"invalid from (RFC3339)"}`, http.StatusBadRequest)
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		http.Error(w, `{"error":"invalid to (RFC3339)"}`, http.StatusBadRequest)
		return
	}
	if to.Before(from) {
		http.Error(w, `{"error":"to must be after from"}`, http.StatusBadRequest)
		return
	}

	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source != "" {
		source = dbpkg.CanonicalSource(source)
	}

	fieldsStr := strings.TrimSpace(r.URL.Query().Get("fields"))
	var requireFields []string
	if fieldsStr != "" {
		for _, f := range strings.Split(fieldsStr, ",") {
			k := strings.TrimSpace(f)
			if k != "" {
				requireFields = append(requireFields, k)
			}
		}
	}

	qctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	limit := 5000
	if limStr := strings.TrimSpace(r.URL.Query().Get("limit")); limStr != "" {
		if n, err := strconv.Atoi(limStr); err == nil {
			limit = n
		}
	}
	rows, err := dbpkg.QueryLatestAircraftReports(qctx, db, dbpkg.HistoryQuery{Source: source, From: from, To: to, Limit: limit, RequireFields: requireFields})
	cancel()
	if err != nil {
		log.Printf("history latest query: %v", err)
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}

	// Always return all DB fields for offline UI.
	resp := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		m := map[string]any{
			"icao":         a.ICAO,
			"lat":          a.Latitude,
			"lon":          a.Longitude,
			"alt":          a.Altitude,
			"speed":        a.Speed,
			"heading":      a.Heading,
			"callsign":     a.Callsign,
			"squawk":       a.Squawk,
			"rssi":         a.RSSI,
			"verticalRate": a.VerticalRate,
			"messages":     a.Messages,
			"onGround":     a.OnGround,
			"source":       a.Source,
			"origin":       a.Origin,
			"geoAltFt":     a.GeoAlt,
			"baroAltFt":    a.BaroAlt,
			"velocityMs":   a.Velocity,
			"seen":         a.Seen.UTC().Format(time.RFC3339),
		}
		resp = append(resp, m)
	}

	_ = json.NewEncoder(w).Encode(resp)
}

// sseWriter wraps an http.ResponseWriter and formats writes as SSE data lines.
type sseWriter struct {
	w http.ResponseWriter
}

func (s *sseWriter) Write(p []byte) (int, error) {
	// encoder writes JSON like {..}\n; convert to SSE: "data: <json>\n\n"
	// trim trailing newline
	bs := p
	if len(bs) > 0 && bs[len(bs)-1] == '\n' {
		bs = bs[:len(bs)-1]
	}
	data := append([]byte("data: "), bs...)
	data = append(data, '\n', '\n')
	return s.w.Write(data)
}

// handleStream handles HTTP GET requests for Server-Sent Events (SSE) streaming.
// Returns a stream of aircraft position updates until the client disconnects.
func (b *Broadcaster) handleStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	setStreamHeaders(w)

	ch := b.Subscribe(ctx)
	flusher := w.(http.Flusher)
	enc := json.NewEncoder(&sseWriter{w: w})

	for a := range ch {
		if err := enc.Encode(a); err != nil {
			return
		}
		flusher.Flush()
	}
}

// handleIngest handles HTTP POST requests to ingest new aircraft position data.
// Expects JSON-encoded Aircraft objects and broadcasts them to all subscribers.
func (b *Broadcaster) handleIngest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Read and parse JSON request body
	var a adsb.Aircraft
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	log.Printf("[SERVER] ingest raw: %s", string(body))
	if err := json.Unmarshal(body, &a); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Normalize fields
	if a.Seen.IsZero() {
		a.Seen = time.Now()
	}
	a.Source = dbpkg.CanonicalSource(a.Source)

	fmt.Printf("[SERVER] Received aircraft: %s source=%s at %.2f,%.2f\n", a.ICAO, a.Source, a.Latitude, a.Longitude)

	// Persist (best-effort)
	b.mu.Lock()
	db := b.db
	b.mu.Unlock()
	if db != nil {
		pctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = dbpkg.InsertAircraftReport(pctx, db, a)
		_ = dbpkg.UpsertAircraft(pctx, db, a)
		cancel()
	}

	b.Broadcast(a)
	w.WriteHeader(http.StatusAccepted)
}

// handleHistoryJSON returns historical ADS-B reports from Postgres as JSON.
// Query params:
//   - source: antenna|simulator|internet
//   - from: RFC3339
//   - to: RFC3339
//   - fields: comma-separated; interpreted as filter criteria (field must be present).
//     Examples: callsign,squawk,origin_country
func (b *Broadcaster) handleHistoryJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	b.mu.Lock()
	db := b.db
	b.mu.Unlock()
	if db == nil {
		http.Error(w, `{"error":"postgres not configured"}`, http.StatusServiceUnavailable)
		return
	}

	fromStr := strings.TrimSpace(r.URL.Query().Get("from"))
	toStr := strings.TrimSpace(r.URL.Query().Get("to"))
	if fromStr == "" || toStr == "" {
		http.Error(w, `{"error":"from and to are required (RFC3339)"}`, http.StatusBadRequest)
		return
	}
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		http.Error(w, `{"error":"invalid from (RFC3339)"}`, http.StatusBadRequest)
		return
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		http.Error(w, `{"error":"invalid to (RFC3339)"}`, http.StatusBadRequest)
		return
	}
	if to.Before(from) {
		http.Error(w, `{"error":"to must be after from"}`, http.StatusBadRequest)
		return
	}

	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source != "" {
		source = dbpkg.CanonicalSource(source)
	}

	fieldsStr := strings.TrimSpace(r.URL.Query().Get("fields"))
	var requireFields []string
	if fieldsStr != "" {
		for _, f := range strings.Split(fieldsStr, ",") {
			k := strings.TrimSpace(f)
			if k != "" {
				requireFields = append(requireFields, k)
			}
		}
	}

	qctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	limit := 50000
	if limStr := strings.TrimSpace(r.URL.Query().Get("limit")); limStr != "" {
		if n, err := strconv.Atoi(limStr); err == nil {
			limit = n
		}
	}
	rows, err := dbpkg.QueryAircraftReports(qctx, db, dbpkg.HistoryQuery{Source: source, From: from, To: to, Limit: limit, RequireFields: requireFields})
	cancel()
	if err != nil {
		log.Printf("history query: %v", err)
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}

	// Always return all DB fields for offline UI.
	resp := make([]map[string]any, 0, len(rows))
	for _, a := range rows {
		m := map[string]any{
			"icao":         a.ICAO,
			"lat":          a.Latitude,
			"lon":          a.Longitude,
			"alt":          a.Altitude,
			"speed":        a.Speed,
			"heading":      a.Heading,
			"callsign":     a.Callsign,
			"squawk":       a.Squawk,
			"rssi":         a.RSSI,
			"verticalRate": a.VerticalRate,
			"messages":     a.Messages,
			"onGround":     a.OnGround,
			"source":       a.Source,
			"origin":       a.Origin,
			"geoAltFt":     a.GeoAlt,
			"baroAltFt":    a.BaroAlt,
			"velocityMs":   a.Velocity,
			"seen":         a.Seen.UTC().Format(time.RFC3339),
		}
		resp = append(resp, m)
	}

	_ = json.NewEncoder(w).Encode(resp)
}

// handleDebug returns the current number of connected subscribers.
func (b *Broadcaster) handleDebug(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "text/plain")

	b.mu.Lock()
	count := len(b.clients)
	b.mu.Unlock()

	fmt.Fprintf(w, "Active subscribers: %d\n", count)
}

// handleHealth returns a simple health check response.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// setStreamHeaders configures HTTP headers for Server-Sent Events.
func setStreamHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

// serveHTTP starts the HTTP server and blocks until shutdown.
func (b *Broadcaster) serveHTTP(addr string, handler http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: handler}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		fmt.Printf("[gRPC] HTTP server started on %s\n", addr)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("http serve: %v", err)
		}
	}()
	return nil
}

// handleAircraftJSON returns all current aircraft as JSON for HTMX requests.
func (b *Broadcaster) handleAircraftJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	b.mu.Lock()
	aircraft := make([]adsb.Aircraft, 0, len(b.aircraft))
	for _, a := range b.aircraft {
		aircraft = append(aircraft, a)
	}
	b.mu.Unlock()

	json.NewEncoder(w).Encode(aircraft)
}

// handleAircraftRows returns aircraft as HTML rows for HTMX table updates.
// Formats each aircraft as a table row that can be swapped in.
// Supports sorting via ?sort=column&asc=true query parameters
func (b *Broadcaster) handleAircraftRows(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, hx-request, hx-trigger, hx-target, hx-current-url, hx-prompt")
	w.Header().Set("Content-Type", "text/html")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	b.mu.Lock()
	aircraft := make([]adsb.Aircraft, 0, len(b.aircraft))
	for _, a := range b.aircraft {
		aircraft = append(aircraft, a)
	}
	b.mu.Unlock()

	// Parse sorting parameters from query string
	sortBy := r.URL.Query().Get("sort")
	ascStr := r.URL.Query().Get("asc")
	ascending := ascStr == "true" // Default to false if not specified, or true if "true"

	fmt.Printf("[SORT-DEBUG] Received sort='%s', asc='%s', ascending=%v, count=%d\n", sortBy, ascStr, ascending, len(aircraft))

	// Sort aircraft based on sort parameter
	switch sortBy {
	case "id":
		sort.Slice(aircraft, func(i, j int) bool {
			if ascending {
				return aircraft[i].ICAO < aircraft[j].ICAO
			}
			return aircraft[i].ICAO > aircraft[j].ICAO
		})
	case "callsign":
		sort.Slice(aircraft, func(i, j int) bool {
			if ascending {
				return aircraft[i].Callsign < aircraft[j].Callsign
			}
			return aircraft[i].Callsign > aircraft[j].Callsign
		})
	case "alt":
		sort.Slice(aircraft, func(i, j int) bool {
			if ascending {
				return aircraft[i].Altitude < aircraft[j].Altitude
			}
			return aircraft[i].Altitude > aircraft[j].Altitude
		})
	case "spd":
		sort.Slice(aircraft, func(i, j int) bool {
			if ascending {
				return aircraft[i].Speed < aircraft[j].Speed
			}
			return aircraft[i].Speed > aircraft[j].Speed
		})
	case "hdg":
		sort.Slice(aircraft, func(i, j int) bool {
			if ascending {
				return aircraft[i].Heading < aircraft[j].Heading
			}
			return aircraft[i].Heading > aircraft[j].Heading
		})
	case "squawk":
		sort.Slice(aircraft, func(i, j int) bool {
			if ascending {
				return aircraft[i].Squawk < aircraft[j].Squawk
			}
			return aircraft[i].Squawk > aircraft[j].Squawk
		})
	case "rssi":
		sort.Slice(aircraft, func(i, j int) bool {
			if ascending {
				return aircraft[i].RSSI < aircraft[j].RSSI
			}
			return aircraft[i].RSSI > aircraft[j].RSSI
		})
	}

	if sortBy != "" {
		// Log first 3 aircraft after sorting for verification
		fmt.Printf("[SORT-DEBUG] After sorting by %s (asc=%v):\n", sortBy, ascending)
		for i := 0; i < 3 && i < len(aircraft); i++ {
			fmt.Printf("  [%d] ICAO=%s Callsign=%s Alt=%d Spd=%d\n",
				i, aircraft[i].ICAO, aircraft[i].Callsign, aircraft[i].Altitude, aircraft[i].Speed)
		}
	}

	// Generate HTML rows with all 8 columns: ICAO, Callsign, Alt, Spd, Hdg, SQK, RSSI, Pred
	for _, a := range aircraft {
		callsign := a.Callsign
		if callsign == "" {
			callsign = "-"
		}
		squawk := a.Squawk
		if squawk == "" {
			squawk = "-"
		}
		seenStr := ""
		if !a.Seen.IsZero() {
			seenStr = a.Seen.UTC().Format(time.RFC3339)
		}
		source := a.Source
		if source == "" {
			source = "unknown"
		}

		sourceKey := strings.ToLower(strings.TrimSpace(source))
		rowAccent := ""
		callsignBadge := ""
		switch sourceKey {
		case "sim", "simulator", "unknown", "":
			rowAccent = "border-left:3px solid #c5483f;"
			callsignBadge = `<span style="display:inline-block;min-width:34px;padding:1px 4px;margin-right:6px;border-radius:4px;background:rgba(197,72,63,0.25);border:1px solid rgba(197,72,63,0.65);color:#ffb3ad;font-size:10px;letter-spacing:0.5px;">SIM</span>`
		case "internet", "net", "api", "online", "dump1090", "readsb", "antenna", "rtlsdr", "rtl":
			rowAccent = "border-left:3px solid #1e7ec8;"
			callsignBadge = `<span style="display:inline-block;min-width:34px;padding:1px 4px;margin-right:6px;border-radius:4px;background:rgba(30,126,200,0.22);border:1px solid rgba(30,126,200,0.7);color:#b7d9ff;font-size:10px;letter-spacing:0.5px;">NET</span>`
		default:
			// fallback: treat as simulator-style
			rowAccent = "border-left:3px solid #c5483f;"
			callsignBadge = `<span style="display:inline-block;min-width:34px;padding:1px 4px;margin-right:6px;border-radius:4px;background:rgba(197,72,63,0.25);border:1px solid rgba(197,72,63,0.65);color:#ffb3ad;font-size:10px;letter-spacing:0.5px;">SIM</span>`
		}
		sourceAttr := html.EscapeString(source)
		callsignAttr := html.EscapeString(callsign)
		squawkAttr := html.EscapeString(squawk)
		seenAttr := html.EscapeString(seenStr)
		originAttr := html.EscapeString(a.Origin)
		rssi := fmt.Sprintf("%.1f", a.RSSI)
		predicted := "N" // Can be extended for prediction logic

		fmt.Fprintf(w, `<tr id="aircraft-%s" data-lat="%.5f" data-lon="%.5f" data-source="%s" data-seen="%s" data-callsign="%s" data-squawk="%s" data-rssi="%s" data-heading="%d" data-track="%d" data-vertical-rate="%d" data-messages="%d" data-on-ground="%t" data-origin="%s" data-geo-alt-ft="%d" data-baro-alt-ft="%d" data-velocity-ms="%.3f" style="background-color:#2d3748;color:#b7d9ff;border-bottom:1px solid #3d4959;%s">
  <td>%s</td>
	<td>%s%s</td>
  <td style="text-align:right;">%d</td>
  <td style="text-align:right;">%d</td>
  <td style="text-align:right;">%d</td>
  <td style="text-align:right;">%s</td>
  <td style="text-align:right;">%s</td>
  <td style="text-align:center;">%s</td>
</tr>
`, a.ICAO, a.Latitude, a.Longitude, sourceAttr, seenAttr, callsignAttr, squawkAttr, rssi, a.Heading, a.Track, a.VerticalRate, a.Messages, a.OnGround, originAttr, a.GeoAlt, a.BaroAlt, a.Velocity,
			rowAccent, a.ICAO, callsignBadge, callsignAttr, a.Altitude, a.Speed, a.Heading, squawkAttr, rssi, predicted)
	}
}
