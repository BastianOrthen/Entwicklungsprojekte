package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/basti/adsb-system/internal/adsb"
	dbpkg "github.com/basti/adsb-system/internal/db"
	grpcserver "github.com/basti/adsb-system/internal/grpc"
)

// Config holds the server configuration from flags/env vars.
type Config struct {
	HTTPAddr    string
	DumpURL     string
	PostgresDSN string
}

func main() {
	cfg := parseConfig()

	fmt.Printf("[SERVER] Starting on %s\n", cfg.HTTPAddr)

	// Initialize broadcaster and start HTTP server
	broadcaster := grpcserver.NewBroadcaster()
	if err := broadcaster.StartHTTP(cfg.HTTPAddr); err != nil {
		fmt.Printf("[SERVER] FATAL: start http: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("[SERVER] HTTP server started. Accepting /ingest and /stream requests.")

	// Initialize database connection if configured
	var dbConn *sql.DB
	if cfg.PostgresDSN != "" {
		var err error
		dbConn, err = dbpkg.Connect(cfg.PostgresDSN)
		if err != nil {
			fmt.Printf("[SERVER] postgres connect: %v\n", err)
		} else if err := dbpkg.EnsureSchema(dbConn); err != nil {
			fmt.Printf("[SERVER] ensure schema: %v\n", err)
		}
	}

	// Setup graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Start data polling and forwarding
	startDataForwarding(ctx, cfg, broadcaster, dbConn)

	// Wait for shutdown signal
	fmt.Println("[SERVER] Running. Press Ctrl+C to stop.")
	<-ctx.Done()
	fmt.Println("[SERVER] Shutting down")
	time.Sleep(200 * time.Millisecond)
}

// parseConfig reads configuration from command-line flags and environment variables.
func parseConfig() Config {
	var cfg Config
	flag.StringVar(&cfg.DumpURL, "dump", "http://127.0.0.1:8080/data/aircraft.json",
		"dump1090 aircraft.json URL")
	flag.StringVar(&cfg.PostgresDSN, "pg", "",
		"postgres dsn (optional)")
	flag.StringVar(&cfg.HTTPAddr, "http", ":8080",
		"http listen address for server (SSE/ingest)")
	flag.Parse()

	// Allow environment variable override for Postgres DSN
	if cfg.PostgresDSN == "" {
		cfg.PostgresDSN = os.Getenv("POSTGRES_DSN")
	}

	return cfg
}

// startDataForwarding runs the main data pipeline: fetch -> broadcast -> persist.
// Runs in a goroutine and respects context cancellation.
func startDataForwarding(ctx context.Context, cfg Config, broadcaster *grpcserver.Broadcaster, dbConn *sql.DB) {
	go func() {
		defer func() {
			fmt.Println("[SERVER] data forwarding loop shutting down")
		}()

		// Start polling if not using default URL
		var ch <-chan adsb.Aircraft
		if cfg.DumpURL != "http://127.0.0.1:8080/data/aircraft.json" {
			fmt.Printf("[SERVER] Starting dump1090 poll: %s\n", cfg.DumpURL)
			ch = adsb.RunDump1090Poll(ctx, cfg.DumpURL)
		} else {
			// Closed channel for default case
			closedCh := make(chan adsb.Aircraft)
			close(closedCh)
			ch = closedCh
		}

		for {
			select {
			case <-ctx.Done():
				return
			case a, ok := <-ch:
				if !ok {
					time.Sleep(100 * time.Millisecond)
					continue
				}
				fmt.Printf("[SERVER] Broadcast: %s\n", a.ICAO)
				broadcaster.Broadcast(a)

				// Persist to database if available
				if dbConn != nil {
					if err := dbpkg.UpsertAircraft(ctx, dbConn, a); err != nil {
						fmt.Printf("[SERVER] db upsert: %v\n", err)
					}
				}
			}
		}
	}()
}
