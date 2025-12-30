package sim

import (
	"math"
	"math/rand"
	"strings"

	"github.com/basti/adsb-system/internal/adsb"
)

// MovementUpdater updates aircraft positions and characteristics over time.
type MovementUpdater struct{}

// NewMovementUpdater creates a new movement updater.
func NewMovementUpdater() *MovementUpdater {
	return &MovementUpdater{}
}

// UpdatePosition advances an aircraft's position by one tick (1 second).
// Applies realistic movement based on heading and speed, with small random variations.
func (mu *MovementUpdater) UpdatePosition(a *adsb.Aircraft) {
	// Apply random heading/speed changes for realism
	if rand.Float64() < 0.05 {
		a.Heading = (a.Heading + rand.Intn(11) - 5 + 360) % 360
	}
	if rand.Float64() < 0.1 {
		a.Speed += rand.Intn(11) - 5
		if a.Speed < 0 {
			a.Speed = 0
		}
	}

	// Calculate new position using great-circle formula
	// 1 knot = 0.514444 m/s
	speedMps := float64(a.Speed) * 0.514444
	dist := speedMps * 1.0 // 1 second per tick
	lat, lon := destPoint(a.Latitude, a.Longitude, float64(a.Heading), dist)
	a.Latitude = lat
	a.Longitude = lon
}

// UpdateAltitude changes aircraft altitude based on whether it's a fighter or civilian.
// Fighters perform rapid maneuvers, civilian aircraft have gentle wobbles.
func (mu *MovementUpdater) UpdateAltitude(a *adsb.Aircraft) {
	if strings.HasPrefix(a.Callsign, "F") {
		// Fighter jets: rapid altitude changes
		dAlt := rand.Intn(4001) - 2000 // -2000 to +2000 ft per second
		a.Altitude += dAlt
		a.VerticalRate = dAlt
	} else {
		// Civilian aircraft: small altitude wobble
		if rand.Float64() < 0.1 {
			a.Altitude += rand.Intn(201) - 100
			if a.Altitude < 0 {
				a.Altitude = 0
			}
			a.VerticalRate = 0
		}
	}

	// Keep altitude within realistic bounds
	if a.Altitude < 0 {
		a.Altitude = 0
	} else if a.Altitude > 45000 {
		a.Altitude = 45000
	}
}

// UpdateFull updates all position and altitude fields for an aircraft in one tick.
func (mu *MovementUpdater) UpdateFull(a *adsb.Aircraft) {
	mu.UpdatePosition(a)
	mu.UpdateAltitude(a)
}

// destPoint calculates the destination latitude/longitude after moving a distance
// from the starting point in a given bearing direction.
// Uses the haversine formula for great-circle distance.
func destPoint(startLat, startLon, bearingDeg, distanceMeters float64) (float64, float64) {
	const R = 6371000.0 // Earth radius in meters
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
