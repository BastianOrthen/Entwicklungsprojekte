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

func main() {
	var dumpURL string
	var pgDsn string
	var httpAddr string
	flag.StringVar(&dumpURL, "dump", "http://127.0.0.1:8080/data/aircraft.json", "dump1090 aircraft.json URL")
	flag.StringVar(&pgDsn, "pg", "", "postgres dsn (optional)")
	flag.StringVar(&httpAddr, "http", ":8080", "http listen address for server (SSE/ingest)")
	flag.Parse()

	fmt.Printf("[SERVER] Starting on %s\n", httpAddr)

	b := grpcserver.NewBroadcaster()

	if err := b.StartHTTP(httpAddr); err != nil {
		fmt.Printf("[SERVER] FATAL: start http: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("[SERVER] HTTP server started. Accepting /ingest and /stream requests.")

	var dbConn *sql.DB
	if pgDsn == "" {
		pgDsn = os.Getenv("POSTGRES_DSN")
	}
	if pgDsn != "" {
		var err error
		dbConn, err = dbpkg.Connect(pgDsn)
		if err != nil {
			fmt.Printf("[SERVER] postgres connect: %v\n", err)
		} else {
			if err := dbpkg.EnsureSchema(dbConn); err != nil {
				fmt.Printf("[SERVER] ensure schema: %v\n", err)
			}
		}
	}

	// Setup interrupt handler
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Optional: polling dump1090
	var ch <-chan adsb.Aircraft
	if dumpURL != "http://127.0.0.1:8080/data/aircraft.json" {
		fmt.Printf("[SERVER] Starting dump1090 poll: %s\n", dumpURL)
		ch = adsb.RunDump1090Poll(ctx, dumpURL)
	} else {
		// Default URL, don't poll
		closedCh := make(chan adsb.Aircraft)
		close(closedCh)
		ch = closedCh
	}

	// Goroutine: forward aircraft from parser to broadcaster
	go func() {
		for {
			select {
			case <-ctx.Done():
				fmt.Println("[SERVER] parser loop shutting down")
				return
			case a, ok := <-ch:
				if !ok {
					time.Sleep(100 * time.Millisecond)
					continue
				}
				fmt.Printf("[SERVER] Broadcast: %s\n", a.ICAO)
				b.Broadcast(a)
				if dbConn != nil {
					if err := dbpkg.UpsertAircraft(ctx, dbConn, a); err != nil {
						fmt.Printf("[SERVER] db upsert: %v\n", err)
					}
				}
			}
		}
	}()

	// Main: wait for interrupt
	fmt.Println("[SERVER] Running. Press Ctrl+C to stop.")
	<-ctx.Done()
	fmt.Println("[SERVER] Shutting down")
	time.Sleep(200 * time.Millisecond)
}
