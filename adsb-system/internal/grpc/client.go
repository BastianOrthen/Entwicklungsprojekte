package grpc

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/basti/adsb-system/internal/adsb"
	dbpkg "github.com/basti/adsb-system/internal/db"
)

// RunSSEClient connects to an SSE/JSON stream at streamURL, forwards messages into Postgres
// using the provided *sql.DB and forwards to webEndpoint (HTTP POST JSON).
func RunSSEClient(ctx context.Context, streamURL string, db *sql.DB, webEndpoint string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	go func() {
		defer resp.Body.Close()
		r := bufio.NewReader(resp.Body)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line, err := r.ReadBytes('\n')
			if err != nil {
				if err == io.EOF {
					return
				}
				log.Printf("read stream: %v", err)
				return
			}
			// trim whitespace
			s := strings.TrimSpace(string(line))
			if s == "" {
				continue
			}
			var a adsb.Aircraft
			if err := json.Unmarshal([]byte(s), &a); err != nil {
				log.Printf("json unmarshal: %v", err)
				continue
			}
			// store to postgres
			if db != nil {
				if err := dbpkg.UpsertAircraft(ctx, db, a); err != nil {
					log.Printf("db upsert: %v", err)
				}
			}
			// forward to web
			if webEndpoint != "" {
				b, _ := json.Marshal(a)
				_, _ = client.Post(webEndpoint, "application/json", strings.NewReader(string(b)))
			}
		}
	}()
	return nil
}
