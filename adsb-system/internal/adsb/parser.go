// Package adsb provides data structures and utilities for handling ADS-B aircraft tracking data.
package adsb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

// RunDump1090Poll periodically fetches aircraft data from a dump1090 JSON endpoint
// and sends updates on the returned channel. The function runs in a goroutine.
// Polling stops when ctx is cancelled.
//
// The endpoint should return JSON in the format: {\"aircraft\": [{\"hex\": \"...\", \"lat\": 50.0, ...}]}
func RunDump1090Poll(ctx context.Context, url string) <-chan Aircraft {
	out := make(chan Aircraft)
	if url == "" {
		url = "http://127.0.0.1:8080/data/aircraft.json"
	}

	go func() {
		defer close(out)
		client := &http.Client{Timeout: 5 * time.Second}
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			func() {
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					return
				}
				var doc struct {
					Aircraft []map[string]interface{} `json:"aircraft"`
				}
				if err := json.Unmarshal(body, &doc); err != nil {
					return
				}
				now := time.Now()
				for _, a := range doc.Aircraft {
					acft := parseAircraft(a, now)
					if acft.ICAO != "" && (acft.Latitude != 0 || acft.Longitude != 0) {
						out <- acft
					}
				}
			}()
		}
	}()

	return out
}

// parseAircraft extracts relevant aircraft data from a raw JSON object from dump1090.
// It handles type conversions and defaults for missing fields.
func parseAircraft(raw map[string]interface{}, now time.Time) Aircraft {
	icao, _ := raw["hex"].(string)
	lat, _ := raw["lat"].(float64)
	lon, _ := raw["lon"].(float64)
	alt := parseInt(raw["altitude"])
	spd := parseInt(raw["gs"])

	return Aircraft{
		ICAO:      icao,
		Latitude:  lat,
		Longitude: lon,
		Altitude:  alt,
		Speed:     spd,
		Seen:      now,
	}
}

// parseInt safely extracts an integer from various types (float64, string, int).
func parseInt(v interface{}) int {
	if v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		if n, err := strconv.Atoi(t); err == nil {
			return n
		}
	case int:
		return t
	}
	return 0}