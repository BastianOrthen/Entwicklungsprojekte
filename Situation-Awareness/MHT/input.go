package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

// runDFInput connects to the MDS DF TCP port and pumps every line into
// the tracker. It transparently reconnects with exponential backoff.
func runDFInput(ctx context.Context, addr string, tracker *Tracker) error {
	delay := 1 * time.Second
	const maxDelay = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		conn, err := dialDF(ctx, addr)
		if err != nil {
			log.Printf("df-tracker: connect to %s failed: %v — retry in %v", addr, err, delay)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
			continue
		}
		log.Printf("df-tracker: connected to MDS at %s", addr)
		delay = 1 * time.Second

		if err := pumpDF(ctx, conn, tracker); err != nil && ctx.Err() == nil {
			log.Printf("df-tracker: input stream lost: %v — reconnecting", err)
		}
		_ = conn.Close()
	}
}

func dialDF(ctx context.Context, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}
	return c, nil
}

func pumpDF(ctx context.Context, conn net.Conn, tracker *Tracker) error {
	const readIdleTimeout = 60 * time.Second
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Decouple TCP read from tracker compute: a bounded queue + worker
	// goroutine ensures that even if OnObservation is momentarily slow,
	// the socket keeps being drained so the upstream fanout never marks
	// us as a slow consumer and disconnects.
	const queueSize = 4096
	q := make(chan *DFObservation, queueSize)
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var dropped, processed uint64
	go func() {
		for {
			select {
			case <-workerCtx.Done():
				return
			case obs := <-q:
				tracker.OnObservation(obs)
				processed++
			}
		}
	}()
	statsTicker := time.NewTicker(5 * time.Second)
	defer statsTicker.Stop()
	go func() {
		var lastProc uint64
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-statsTicker.C:
				log.Printf("df-tracker: ingest — processed=%d (Δ%d), dropped=%d, queue=%d/%d",
					processed, processed-lastProc, dropped, len(q), cap(q))
				lastProc = processed
			}
		}
	}()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = conn.SetReadDeadline(time.Now().Add(readIdleTimeout))

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read: %w", err)
			}
			return fmt.Errorf("peer closed stream")
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "PING" {
			continue
		}
		var obs DFObservation
		if err := json.Unmarshal([]byte(line), &obs); err != nil {
			// Don't log every malformed line — DF stream is high-rate.
			continue
		}
		select {
		case q <- &obs:
		default:
			dropped++
		}
	}
}
