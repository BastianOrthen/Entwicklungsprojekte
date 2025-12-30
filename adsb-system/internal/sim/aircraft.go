// Package sim provides aircraft simulation and movement calculations.
package sim

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/basti/adsb-system/internal/adsb"
)

// AircraftGenerator creates new simulated aircraft.
type AircraftGenerator struct {
	fighterCount int
}

// NewAircraftGenerator creates a generator that produces random aircraft.
func NewAircraftGenerator() *AircraftGenerator {
	return &AircraftGenerator{}
}

// GenerateFleet creates a fleet of random aircraft including both civilian and fighter jets.
// fleetSize: number of civilian aircraft
// fighterSize: number of fighter jets
func (ag *AircraftGenerator) GenerateFleet(fleetSize, fighterSize int) []adsb.Aircraft {
	fleet := make([]adsb.Aircraft, 0, fleetSize+fighterSize)

	// Generate civilian aircraft
	for i := 0; i < fleetSize; i++ {
		fleet = append(fleet, ag.generateCivilian())
	}

	// Generate fighter jets
	for i := 0; i < fighterSize; i++ {
		fighter := ag.generateCivilian()
		fighter.Speed = 300 + rand.Intn(400) // faster than civilian
		fighter.Callsign = fmt.Sprintf("F%d", i+1)
		fleet = append(fleet, fighter)
	}

	return fleet
}

// generateCivilian creates a random civilian aircraft with realistic initial values.
func (ag *AircraftGenerator) generateCivilian() adsb.Aircraft {
	return adsb.Aircraft{
		ICAO:         randomICAO(),
		Latitude:     50.0 + (rand.Float64()-0.5)*6.0, // Central Europe
		Longitude:    8.0 + (rand.Float64()-0.5)*8.0,  // Central Europe
		Altitude:     2000 + rand.Intn(30000),
		Speed:        100 + rand.Intn(400),
		Heading:      rand.Intn(360),
		Track:        rand.Intn(360),
		Callsign:     randomCallsign(),
		Squawk:       randomSquawk(),
		VerticalRate: 0,
		Messages:     1,
		RSSI:         -5.0 + rand.Float64()*5.0,
		OnGround:     false,
		Source:       "sim",
		Seen:         time.Now(),
	}
}

// randomICAO generates a random 6-character hex ICAO code.
func randomICAO() string {
	const letters = "0123456789ABCDEF"
	b := make([]byte, 6)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// randomCallsign generates an airline-style callsign (e.g., DLH123).
func randomCallsign() string {
	letters := []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	a := letters[rand.Intn(len(letters))]
	b := letters[rand.Intn(len(letters))]
	num := 100 + rand.Intn(900)
	return fmt.Sprintf("%c%c%d", a, b, num)
}

// randomSquawk generates a random 4-digit transponder code.
func randomSquawk() string {
	return fmt.Sprintf("%04d", rand.Intn(10000))
}
