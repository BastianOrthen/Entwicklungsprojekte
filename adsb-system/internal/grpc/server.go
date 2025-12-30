package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/basti/adsb-system/internal/adsb"
)

// Broadcaster provides a simple in-memory broadcast for Aircraft messages.
type Broadcaster struct {
	mu      sync.Mutex
	clients map[chan adsb.Aircraft]struct{}
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{clients: make(map[chan adsb.Aircraft]struct{})}
}

// Subscribe returns a channel that will receive aircraft updates until ctx is done.
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

// Broadcast sends an Aircraft to all subscribers (non-blocking).
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

// StartHTTP starts a simple HTTP server that exposes Server-Sent Events at /stream
// and a health endpoint. This is useful for web clients and for bridging to other
// components; it complements the gRPC stream.
func (b *Broadcaster) StartHTTP(addr string) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		ch := b.Subscribe(ctx)
		flusher, _ := w.(http.Flusher)
		enc := json.NewEncoder(&sseWriter{w: w})
		for a := range ch {
			// send proper SSE "data: <json>\n\n"
			if err := enc.Encode(a); err != nil {
				return
			}
			flusher.Flush()
		}
	})
	// ingest accepts JSON aircraft via POST and broadcasts to subscribers.
	mux.HandleFunc("/ingest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
			var a adsb.Aircraft
			// read raw body for debugging and to ensure we log all fields
			body := []byte{}
			if r.Body != nil {
				if b, err := io.ReadAll(r.Body); err == nil {
					body = b
				}
			}
			if len(body) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			log.Printf("[SERVER] ingest raw: %s", string(body))
			if err := json.Unmarshal(body, &a); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fmt.Printf("[SERVER] Received aircraft: %s at %.2f,%.2f\n", a.ICAO, a.Latitude, a.Longitude)
		b.Broadcast(a)
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Debug endpoint: show number of subscribers
	mux.HandleFunc("/debug", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		b.mu.Lock()
		count := len(b.clients)
		b.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "Active subscribers: %d\n", count)
	})

	srv := &http.Server{Addr: addr, Handler: mux}
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
