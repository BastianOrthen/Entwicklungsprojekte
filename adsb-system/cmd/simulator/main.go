package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/basti/adsb-system/internal/adsb"
)

func main() {
	var target string
	flag.StringVar(&target, "target", "http://localhost:8080/ingest", "ingest endpoint for simulated aircraft")
	flag.Parse()

	log.Printf("Simulator starting, posting to: %s", target)

	client := &http.Client{Timeout: 5 * time.Second}
	rand.Seed(time.Now().UnixNano())

	// create N persistent aircraft that move slightly each tick
	const N = 15
	rand.Seed(time.Now().UnixNano())
	planes := make([]adsb.Aircraft, 0, N)
	for i := 0; i < N; i++ {
		planes = append(planes, randomAircraft())
	}
	// add 3 fighter jets that maneuver with rapid altitude changes
	for i := 0; i < 3; i++ {
		fighter := randomAircraft()
		fighter.Speed = 300 + rand.Intn(400) // faster
		fighter.Callsign = fmt.Sprintf("F%d", i+1)
		planes = append(planes, fighter)
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// loop until manual interrupt; update each plane a bit and POST
	for range ticker.C {
		for i := range planes {
			// Move plane by current heading and speed (meters per second)
			// 1 knot = 0.514444 m/s
			dt := 1.0 // seconds per tick
			speedMps := float64(planes[i].Speed) * 0.514444
			dist := speedMps * dt
			// small random drift to heading and speed for realism
			if rand.Float64() < 0.05 {
				planes[i].Heading = (planes[i].Heading + rand.Intn(11) - 5 + 360) % 360
			}
			if rand.Float64() < 0.1 {
				planes[i].Speed += rand.Intn(11) - 5
				if planes[i].Speed < 0 {
					planes[i].Speed = 0
				}
			}
			// compute destination point using great-circle formula
			nl, nol := destPoint(planes[i].Latitude, planes[i].Longitude, float64(planes[i].Heading), dist)
			planes[i].Latitude = nl
			planes[i].Longitude = nol
			
			// altitude changes: normal aircraft wobble, fighters maneuver rapidly
			if strings.HasPrefix(planes[i].Callsign, "F") {
				// fighter jets: rapid altitude changes (+/- 500-2000 ft per second)
				dAlt := rand.Intn(4001) - 2000 // -2000 to +2000 ft per second
				planes[i].Altitude += dAlt
				planes[i].VerticalRate = dAlt
			} else {
				// small altitude wobble
				if rand.Float64() < 0.1 {
					planes[i].Altitude += rand.Intn(201) - 100
					if planes[i].Altitude < 0 {
						planes[i].Altitude = 0
					}
					planes[i].VerticalRate = 0
				}
			}
			// keep altitude in bounds
			if planes[i].Altitude < 0 {
				planes[i].Altitude = 0
			} else if planes[i].Altitude > 45000 {
				planes[i].Altitude = 45000
			}
			planes[i].Seen = time.Now()

			b, _ := json.Marshal(planes[i])
			log.Printf("[SIMPAYLOAD] %s", string(b))
			req, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(string(b)))
			req.Header.Set("Content-Type", "application/json")
			if _, err := client.Do(req); err != nil {
				log.Printf("[SIM] Error posting %s: %v", planes[i].ICAO, err)
			} else {
				log.Printf("[SIM] Posted %s at %.5f,%.5f alt=%d hdg=%d spd=%d", planes[i].ICAO, planes[i].Latitude, planes[i].Longitude, planes[i].Altitude, planes[i].Heading, planes[i].Speed)
			}
		}
	}
}

func randomAircraft() adsb.Aircraft {
	// crude random positions around central Europe
	lat := 50.0 + (rand.Float64()-0.5)*6.0
	lon := 8.0 + (rand.Float64()-0.5)*8.0
	alt := 2000 + rand.Intn(30000)
	spd := 100 + rand.Intn(400)
	icao := randomICAO()
	// assign an initial random heading for smooth motion
	hdg := rand.Intn(360)
	// populate additional ADS-B-like fields for richer simulation
	callsign := randomCallsign()
	squawk := randomSquawk()
	return adsb.Aircraft{
		ICAO:  icao,
		Latitude: lat,
		Longitude: lon,
		Altitude: alt,
		Speed: spd,
		Heading: hdg,
		Track: hdg,
		Callsign: callsign,
		Squawk: squawk,
		VerticalRate: 0,
		Messages: 1,
		RSSI: -5.0 + rand.Float64()*5.0,
		OnGround: false,
		Source: "sim",
		Seen: time.Now(),
	}
}

func randomCallsign() string {
	// simple airline-like callsigns (e.g., DLH123)
	letters := []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	a := letters[rand.Intn(len(letters))]
	b := letters[rand.Intn(len(letters))]
	num := 100 + rand.Intn(900)
	return fmt.Sprintf("%c%c%d", a, b, num)
}

func randomSquawk() string {
	return fmt.Sprintf("%04d", rand.Intn(10000))
}

// destPoint returns the destination lat/lon after moving distance meters from
// startLat,startLon at bearing degrees.
func destPoint(startLat, startLon, bearingDeg, distanceMeters float64) (float64, float64) {
	const R = 6371000.0 // earth radius in meters
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	toDeg := func(r float64) float64 { return r * 180 / math.Pi }

	φ1 := toRad(startLat)
	λ1 := toRad(startLon)
	θ := toRad(bearingDeg)
	δ := distanceMeters / R

	sinφ2 := math.Sin(φ1)*math.Cos(δ) + math.Cos(φ1)*math.Sin(δ)*math.Cos(θ)
	φ2 := math.Asin(sinφ2)
	y := math.Sin(θ) * math.Sin(δ) * math.Cos(φ1)
	x := math.Cos(δ) - math.Sin(φ1)*sinφ2
	λ2 := λ1 + math.Atan2(y, x)

	return toDeg(φ2), toDeg(λ2)
}

func randomICAO() string {
	const letters = "0123456789ABCDEF"
	b := make([]byte, 6)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
