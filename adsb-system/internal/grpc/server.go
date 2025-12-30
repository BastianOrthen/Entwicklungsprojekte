// Package grpc provides HTTP-based streaming (SSE) and ingest endpoints for aircraft data.
// Despite the package name, it currently uses HTTP/SSE rather than gRPC for broader compatibility.
// It implements a broadcaster that maintains connections to multiple clients and streams
// aircraft position updates in real-time.
package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/basti/adsb-system/internal/adsb"
)

// Broadcaster manages real-time distribution of aircraft updates to multiple subscribers.
// It uses an in-memory channel-based system for low-latency streaming.
// Thread-safe and suitable for up to hundreds of concurrent clients.
type Broadcaster struct {
	mu      sync.Mutex
	clients map[chan adsb.Aircraft]struct{}
}

// NewBroadcaster creates a new broadcaster instance.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{clients: make(map[chan adsb.Aircraft]struct{})}
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
func (b *Broadcaster) Broadcast(a adsb.Aircraft) {
	b.mu.Lock()
	defer b.mu.Unlock()
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

	return b.serveHTTP(addr, mux)
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

	fmt.Printf("[SERVER] Received aircraft: %s at %.2f,%.2f\n", a.ICAO, a.Latitude, a.Longitude)
	b.Broadcast(a)
	w.WriteHeader(http.StatusAccepted)
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
