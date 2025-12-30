package adsb

import "time"

// Aircraft represents a simplified ADS-B track for a single aircraft.
type Aircraft struct {
	ICAO      string    `json:"icao" db:"icao"`
	Latitude  float64   `json:"lat" db:"lat"`
	Longitude float64   `json:"lon" db:"lon"`
	Altitude  int       `json:"alt" db:"alt"`
	Speed     int       `json:"speed" db:"speed"`
	Heading   int       `json:"heading,omitempty" db:"heading"`
	// Additional common ADS-B fields
	Callsign    string    `json:"callsign,omitempty" db:"callsign"`
	Squawk      string    `json:"squawk,omitempty" db:"squawk"`
	VerticalRate int      `json:"vertical_rate,omitempty" db:"vertical_rate"`
	Track       int       `json:"track,omitempty" db:"track"`
	Messages    int       `json:"messages,omitempty" db:"messages"`
	RSSI        float64   `json:"rssi,omitempty" db:"rssi"`
	OnGround    bool      `json:"on_ground,omitempty" db:"on_ground"`
	Source      string    `json:"source,omitempty" db:"source"`
	Seen        time.Time `json:"seen" db:"seen"`
}
