// Package adsb provides data structures and utilities for handling ADS-B aircraft tracking data.
// It includes models for aircraft data, parsing from external sources (dump1090), and
// converting raw ADS-B information into a normalized format.
package adsb

import "time"

// Aircraft represents a single aircraft position report from ADS-B.
// It contains both essential fields (ICAO, Latitude, Longitude, Altitude, Speed)
// and optional fields that provide additional context.
type Aircraft struct {
	// Essential tracking fields
	ICAO      string  `json:"icao" db:"icao"`           // ICAO 24-bit address (hex)
	Latitude  float64 `json:"lat" db:"lat"`             // Latitude in degrees
	Longitude float64 `json:"lon" db:"lon"`             // Longitude in degrees
	Altitude  int     `json:"alt" db:"alt"`             // Altitude in feet
	Speed     int     `json:"speed" db:"speed"`         // Ground speed in knots

	// Navigation and identification fields
	Heading      int    `json:"heading,omitempty" db:"heading"`           // Track heading in degrees
	Callsign     string `json:"callsign,omitempty" db:"callsign"`         // Flight callsign (e.g., LH123)
	Squawk       string `json:"squawk,omitempty" db:"squawk"`             // Transponder code
	VerticalRate int    `json:"vertical_rate,omitempty" db:"vertical_rate"` // Climb/descent rate ft/min
	Track        int    `json:"track,omitempty" db:"track"`               // Track angle

	// Receiver signal and metadata
	Messages  int       `json:"messages,omitempty" db:"messages"`   // Number of messages received
	RSSI      float64   `json:"rssi,omitempty" db:"rssi"`           // Signal strength (dBm)
	OnGround  bool      `json:"on_ground,omitempty" db:"on_ground"` // Aircraft on ground
	Source    string    `json:"source,omitempty" db:"source"`       // Data source (sim, dump1090, etc)
	Seen      time.Time `json:"seen" db:"seen"`                     // Last position report time
}
