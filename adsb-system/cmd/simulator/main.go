// Package main provides an aircraft simulator that generates realistic flight paths
// and posts them to the ADSB tracking server.
//
// The simulator:
// - Generates a fleet of 15 random aircraft with varied routes
// - Updates each aircraft's position every 1 second
// - Posts aircraft data to the server's /ingest endpoint via HTTP POST JSON
// - Simulates realistic movement, altitude changes, and speed variations
//
// Usage:
//
//	./simulator -target http://localhost:8080/ingest
package main

import (
	"encoding/json"
	"flag"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/basti/adsb-system/internal/adsb"
	"github.com/basti/adsb-system/internal/sim"
)

func main() {
	var target string
	flag.StringVar(&target, "target", "http://localhost:8080/ingest", "ingest endpoint for simulated aircraft")
	flag.Parse()

	log.Printf("Simulator starting, posting to: %s", target)

	client := &http.Client{Timeout: 5 * time.Second}
	rand.Seed(time.Now().UnixNano())

	// Generate initial aircraft fleet
	generator := sim.NewAircraftGenerator()
	planes := generator.GenerateFleet(15, 3)

	// Start simulation loop
	simulator := NewSimulator(planes, client, target)
	simulator.Run()
}

// Simulator manages the simulation loop for aircraft movement and posting.
type Simulator struct {
	planes  []adsb.Aircraft
	client  *http.Client
	target  string
	updater *sim.MovementUpdater
	ticker  *time.Ticker
}

// NewSimulator creates a new simulator instance.
func NewSimulator(planes []adsb.Aircraft, client *http.Client, target string) *Simulator {
	return &Simulator{
		planes:  planes,
		client:  client,
		target:  target,
		updater: sim.NewMovementUpdater(),
		ticker:  time.NewTicker(1 * time.Second),
	}
}

// Run starts the main simulation loop and posts aircraft data to the server.
// Runs until manually interrupted.
func (s *Simulator) Run() {
	defer s.ticker.Stop()

	for range s.ticker.C {
		s.updateAndPostAircraft()
	}
}

// updateAndPostAircraft updates positions for all aircraft and posts them to the server.
func (s *Simulator) updateAndPostAircraft() {
	for i := range s.planes {
		s.updater.UpdateFull(&s.planes[i])
		s.planes[i].Seen = time.Now()
		s.postAircraft(s.planes[i])
	}
}

// postAircraft sends a single aircraft update to the server via HTTP POST.
func (s *Simulator) postAircraft(a adsb.Aircraft) {
	b, _ := json.Marshal(a)
	log.Printf("[SIMPAYLOAD] %s", string(b))

	req, _ := http.NewRequest(http.MethodPost, s.target, strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	if _, err := s.client.Do(req); err != nil {
		log.Printf("[SIM] Error posting %s: %v", a.ICAO, err)
	} else {
		log.Printf("[SIM] Posted %s at %.5f,%.5f alt=%d hdg=%d spd=%d",
			a.ICAO, a.Latitude, a.Longitude, a.Altitude, a.Heading, a.Speed)
	}
}
