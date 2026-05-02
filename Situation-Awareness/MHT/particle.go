package main

import (
	"math"
	"math/rand"
)

// Motion-model indices for the IMM-style mixed-model PF.
//
// Real RF emitters fall into very different motion regimes: most
// COMINT/ELINT targets in tactical scenarios are static (radar sites,
// jammers, ground TX), some move at constant velocity (vehicles,
// vessels, transport aircraft), and a few maneuver hard (fighters,
// drones in evasive flight). A single CV model has to compromise its
// noise tuning between all three, which is exactly the visible "wackel"
// in the operator UI: stationary tracks acquire random walk velocity,
// fast-mover tracks get pulled back toward zero.
//
// We tag every particle with a model index and use model-specific
// process noise + a small stochastic switching probability per Predict.
// SIR resampling automatically concentrates the cloud on the model that
// fits best — a poor man's IMM that costs almost nothing on top of the
// existing PF.
const (
	ModelStationary uint8 = 0 // ≈0 m/s velocity; tiny pos noise to absorb GPS-style sensor drift
	ModelCV         uint8 = 1 // constant-velocity, moderate noise
	ModelManeuver   uint8 = 2 // high acceleration noise — fighters, evasive drones
	numModels             = 3
)

// modelMix is the prior mixing proportion at track birth. Tactical
// scenarios skew strongly toward stationary; we still seed enough
// CV/Maneuver particles for the resampler to grab onto if the target
// turns out to move. Used as a fallback when the observation carries
// no battle-dimension hint.
var modelMix = [numModels]float64{0.60, 0.32, 0.08}

// motionPriorFor returns the [stationary, CV, maneuver] particle-mix
// prior tuned to the tactical motion regime of the target. Air-fixed
// gets almost all maneuver weight, stationary radars 100 % stationary,
// sea targets a slow-CV bias, etc. Falls back to modelMix for unknown.
func motionPriorFor(dim BattleDim) [numModels]float64 {
	switch dim {
	case DimStationary:
		// Static radar / ground TX: collapse fully onto stationary so
		// the cloud doesn't acquire phantom velocity. Tiny CV dust so
		// the resampler can recover if the emitter unexpectedly drives
		// off (truck-mounted "stationary" sites do exist).
		return [numModels]float64{0.95, 0.04, 0.01}
	case DimSemiMobile:
		// Truck-mounted SAM / mobile EW / ship in port: usually still
		// for tens of minutes, then move 30–60 km/h.
		return [numModels]float64{0.70, 0.28, 0.02}
	case DimGround:
		// Tactical ground vehicles: mostly CV in 10–30 m/s with stops.
		return [numModels]float64{0.30, 0.65, 0.05}
	case DimSea:
		// Surface vessels: reliable slow CV (5–20 m/s), rare turns.
		return [numModels]float64{0.10, 0.85, 0.05}
	case DimAirRotary:
		// Helicopters / multirotor drones: hover capable, moderate
		// agility — heavier maneuver tail than ground/sea.
		return [numModels]float64{0.20, 0.55, 0.25}
	case DimAirFixed:
		// Fixed-wing aircraft / jets: no real "stationary" mode; CV
		// dominates, but maneuver is essential during turns.
		return [numModels]float64{0.02, 0.55, 0.43}
	}
	return modelMix
}

// velSigmaForDim overrides the per-model velocity prior for the given
// battle dimension. Returns 0 to fall back to velocitySigmaForModel.
func velSigmaForDim(dim BattleDim, m uint8) float64 {
	switch dim {
	case DimSea:
		// Ships: 0.5 / 8 / 25 m/s priors (≈ kn 1 / 16 / 50).
		switch m {
		case ModelStationary:
			return 0.5
		case ModelCV:
			return 8
		case ModelManeuver:
			return 25
		}
	case DimGround, DimSemiMobile:
		// Vehicles: 0.5 / 12 / 30 m/s.
		switch m {
		case ModelStationary:
			return 0.5
		case ModelCV:
			return 12
		case ModelManeuver:
			return 30
		}
	case DimAirRotary:
		// Helos / multirotor drones: 1 / 25 / 80 m/s.
		switch m {
		case ModelStationary:
			return 1
		case ModelCV:
			return 25
		case ModelManeuver:
			return 80
		}
	case DimAirFixed:
		// Fast jets: 5 / 150 / 250 m/s — a Su-27 cruises at ~250 m/s.
		switch m {
		case ModelStationary:
			return 5
		case ModelCV:
			return 150
		case ModelManeuver:
			return 250
		}
	}
	return 0
}

// modelSwitchProb is the per-step probability that a particle changes
// its motion model. Small enough that established hypotheses persist,
// large enough to track regime changes (e.g. parked → driving) within
// a few seconds at 2 Hz observation rate.
const modelSwitchProb = 0.01

// Particle represents one hypothesis sample for a track. State is
// position (lat/lon), velocity in metres-east / metres-north per
// second, and the active motion model. The local-tangent-plane
// approximation is good enough for the ranges we deal with here
// (≤300 km).
type Particle struct {
	Lat    float64 // deg
	Lon    float64 // deg
	VxMps  float64 // m/s east
	VyMps  float64 // m/s north
	Weight float64 // ∑ Weight = 1 after normalisation
	Model  uint8   // ModelStationary | ModelCV | ModelManeuver
}

// ParticleFilter is a SIR particle filter with an IMM-style mixed
// motion model. One filter per track.
type ParticleFilter struct {
	Particles []Particle
	rng       *rand.Rand
	// Dim is the cached battle dimension used by Predict to scale the
	// per-model process noise and by stochastic model switches to draw
	// a domain-appropriate velocity prior.
	Dim BattleDim
}

// NewParticleFilterFromBearing seeds N particles uniformly along the
// bearing line from minRange to maxRange. The velocity prior depends
// on the assigned motion model and the battle dimension: a fast-jet
// hypothesis gets a wide velocity tail, a stationary-radar hypothesis
// almost none. Models are mixed according to motionPriorFor(dim).
func NewParticleFilterFromBearing(
	n int,
	sensorLat, sensorLon, bearingDeg, sigmaDeg float64,
	minRangeM, maxRangeM float64,
	dim BattleDim,
	rng *rand.Rand,
) *ParticleFilter {
	if n < 1 {
		n = 1
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(rand.Int63()))
	}

	mix := motionPriorFor(dim)

	// Per-model count from the prior mix (rounded; remainder absorbed
	// into the largest bucket).
	counts := [numModels]int{}
	used := 0
	largest := uint8(0)
	for m := 0; m < numModels; m++ {
		counts[m] = int(float64(n) * mix[m])
		used += counts[m]
		if mix[m] > mix[largest] {
			largest = uint8(m)
		}
	}
	counts[largest] += n - used

	particles := make([]Particle, 0, n)
	for m := 0; m < numModels; m++ {
		velSigma := velSigmaForDim(dim, uint8(m))
		if velSigma == 0 {
			velSigma = velocitySigmaForModel(uint8(m))
		}
		for k := 0; k < counts[m]; k++ {
			r := minRangeM + rng.Float64()*(maxRangeM-minRangeM)
			theta := bearingDeg + rng.NormFloat64()*sigmaDeg
			lat, lon := projectGeo(sensorLat, sensorLon, theta, r)
			var vx, vy float64
			if velSigma > 0 {
				vx = rng.NormFloat64() * velSigma
				vy = rng.NormFloat64() * velSigma
			}
			particles = append(particles, Particle{
				Lat: lat, Lon: lon,
				VxMps: vx, VyMps: vy,
				Weight: 1.0 / float64(n),
				Model:  uint8(m),
			})
		}
	}
	return &ParticleFilter{Particles: particles, rng: rng, Dim: dim}
}

// velocitySigmaForModel returns the 1σ velocity prior in m/s for each
// motion model — used only at seeding and after a model switch.
func velocitySigmaForModel(m uint8) float64 {
	switch m {
	case ModelStationary:
		return 0.5 // essentially still; tiny prior to keep particles diverse
	case ModelCV:
		return 12 // ~25 kn / 45 km/h cruise
	case ModelManeuver:
		return 80 // ~290 km/h fighter / fast drone
	}
	return 10
}

// posNoiseForModel returns the per-second position process noise (m/√s).
// Stationary emitters need only enough noise to absorb sensor jitter;
// maneuvering targets need wide enough kernel for the SIR resampler to
// catch up with hard turns.
func posNoiseForModel(m uint8, base float64) float64 {
	switch m {
	case ModelStationary:
		return 0.10 * base // smooth stationary cloud → smooth output
	case ModelCV:
		return 1.00 * base
	case ModelManeuver:
		return 3.00 * base
	}
	return base
}

// velNoiseForModel returns the per-second velocity process noise (m/s/√s).
// Stationary particles damp velocity back toward zero rather than
// random-walking, which is the dominant cause of jittery tracks on
// static emitters.
func velNoiseForModel(m uint8, base float64) float64 {
	switch m {
	case ModelStationary:
		return 0.05 * base
	case ModelCV:
		return 0.50 * base
	case ModelManeuver:
		return 4.00 * base
	}
	return base
}

// Predict advances all particles by dt seconds with per-model motion
// kinematics and process noise. With probability modelSwitchProb each
// particle additionally jumps to a different model — the SIR resampler
// then concentrates the cloud on whichever model fits best.
func (pf *ParticleFilter) Predict(dt float64, posSigma, velSigma float64) {
	if dt <= 0 || len(pf.Particles) == 0 {
		return
	}
	sqrtDt := math.Sqrt(dt)
	// Stationary particles are pulled toward zero velocity at a
	// half-life of ~5 s — strong enough to kill random-walk drift on
	// static emitters, weak enough that a real start of motion still
	// shows up after a couple of bearing updates (helped along by the
	// stochastic model switch into CV/Maneuver).
	const stationaryDamp = 0.13 // 1 - exp(-dt/5) at dt≈1s

	for i := range pf.Particles {
		p := &pf.Particles[i]

		// Stochastic model switch.
		if pf.rng.Float64() < modelSwitchProb {
			newModel := uint8(pf.rng.Intn(numModels))
			if newModel != p.Model {
				p.Model = newModel
				vs := velSigmaForDim(pf.Dim, newModel)
				if vs == 0 {
					vs = velocitySigmaForModel(newModel)
				}
				if vs > 0 {
					p.VxMps = pf.rng.NormFloat64() * vs
					p.VyMps = pf.rng.NormFloat64() * vs
				} else {
					p.VxMps, p.VyMps = 0, 0
				}
			}
		}

		posStep := posNoiseForModel(p.Model, posSigma) * sqrtDt
		velStep := velNoiseForModel(p.Model, velSigma) * sqrtDt

		// dx (east) = vx*dt + noise, dy (north) = vy*dt + noise
		dx := p.VxMps*dt + pf.rng.NormFloat64()*posStep
		dy := p.VyMps*dt + pf.rng.NormFloat64()*posStep
		p.Lat, p.Lon = offsetGeo(p.Lat, p.Lon, dx, dy)
		p.VxMps += pf.rng.NormFloat64() * velStep
		p.VyMps += pf.rng.NormFloat64() * velStep

		if p.Model == ModelStationary {
			damp := stationaryDamp * dt
			if damp > 1 {
				damp = 1
			}
			p.VxMps -= damp * p.VxMps
			p.VyMps -= damp * p.VyMps
		}
	}
}

// ModelMix returns the current weighted mix of motion models in the
// cloud — useful for diagnostics ("track 4 is 92 % stationary").
func (pf *ParticleFilter) ModelMix() (stat, cv, mvr float64) {
	for _, p := range pf.Particles {
		switch p.Model {
		case ModelStationary:
			stat += p.Weight
		case ModelCV:
			cv += p.Weight
		case ModelManeuver:
			mvr += p.Weight
		}
	}
	return
}

// Update reweights particles by their likelihood under the new bearing
// observation, normalises, and resamples if the effective sample size
// drops below half. Returns the unnormalised total likelihood — useful
// as a track-level "evidence" score for MHT association gating.
func (pf *ParticleFilter) Update(
	sensorLat, sensorLon, observedBearingDeg, sigmaDeg float64,
) float64 {
	if sigmaDeg <= 0 {
		sigmaDeg = 1.0
	}
	// 2σ cap on the bearing residual: anything beyond ±2σ contributes
	// almost zero weight. Without this cap a wildly wrong association
	// can still produce a 1e-30 weight that pollutes the cloud.
	maxResidual := 4 * sigmaDeg

	var totalLikelihood float64
	for i := range pf.Particles {
		p := &pf.Particles[i]
		predicted := bearingFromTo(sensorLat, sensorLon, p.Lat, p.Lon)
		diff := angleDiffDeg(observedBearingDeg, predicted)
		if math.Abs(diff) > maxResidual {
			p.Weight = 0
			continue
		}
		// Gaussian likelihood (drop the constant prefactor — we normalise
		// straight after so it cancels out).
		w := math.Exp(-0.5 * (diff / sigmaDeg) * (diff / sigmaDeg))
		p.Weight *= w
		totalLikelihood += p.Weight
	}

	if totalLikelihood == 0 {
		// Total mismatch — re-uniform to avoid filter death.
		for i := range pf.Particles {
			pf.Particles[i].Weight = 1.0 / float64(len(pf.Particles))
		}
		return 0
	}

	// Normalise.
	for i := range pf.Particles {
		pf.Particles[i].Weight /= totalLikelihood
	}

	if pf.effectiveSampleSize() < float64(len(pf.Particles))/2.0 {
		pf.systematicResample()
	}
	return totalLikelihood
}

// ObservationLikelihood returns the *current* (pre-update) likelihood
// of an observation under the particle cloud — used for MHT gating
// without disturbing the filter state.
func (pf *ParticleFilter) ObservationLikelihood(
	sensorLat, sensorLon, observedBearingDeg, sigmaDeg float64,
) float64 {
	if sigmaDeg <= 0 {
		sigmaDeg = 1.0
	}
	var sum float64
	for i := range pf.Particles {
		p := pf.Particles[i]
		predicted := bearingFromTo(sensorLat, sensorLon, p.Lat, p.Lon)
		diff := angleDiffDeg(observedBearingDeg, predicted)
		w := math.Exp(-0.5 * (diff / sigmaDeg) * (diff / sigmaDeg))
		sum += p.Weight * w
	}
	return sum
}

// MeanState returns the weighted mean position and velocity, plus
// position standard deviation in metres for both axes.
func (pf *ParticleFilter) MeanState() (lat, lon, vx, vy, stdEast, stdNorth float64) {
	if len(pf.Particles) == 0 {
		return
	}
	for _, p := range pf.Particles {
		lat += p.Lat * p.Weight
		lon += p.Lon * p.Weight
		vx += p.VxMps * p.Weight
		vy += p.VyMps * p.Weight
	}
	// Variance of position relative to mean, projected to metres.
	var ve, vn float64
	for _, p := range pf.Particles {
		dx, dy := metresBetween(lat, lon, p.Lat, p.Lon)
		ve += p.Weight * dx * dx
		vn += p.Weight * dy * dy
	}
	stdEast = math.Sqrt(ve)
	stdNorth = math.Sqrt(vn)
	return
}

// effectiveSampleSize is the standard 1 / Σ wᵢ² metric.
func (pf *ParticleFilter) effectiveSampleSize() float64 {
	var s float64
	for _, p := range pf.Particles {
		s += p.Weight * p.Weight
	}
	if s == 0 {
		return 0
	}
	return 1.0 / s
}

// systematicResample draws N new particles with low variance; weights
// are reset to 1/N afterwards.
func (pf *ParticleFilter) systematicResample() {
	n := len(pf.Particles)
	cumulative := make([]float64, n)
	cumulative[0] = pf.Particles[0].Weight
	for i := 1; i < n; i++ {
		cumulative[i] = cumulative[i-1] + pf.Particles[i].Weight
	}
	step := 1.0 / float64(n)
	u := pf.rng.Float64() * step
	resampled := make([]Particle, n)
	j := 0
	for i := 0; i < n; i++ {
		for u > cumulative[j] && j < n-1 {
			j++
		}
		resampled[i] = pf.Particles[j]
		resampled[i].Weight = 1.0 / float64(n)
		u += step
	}
	pf.Particles = resampled
}

// ── Geo helpers (local-tangent-plane approximation) ─────────────────────

const earthRadius = 6378137.0 // WGS84 semi-major (m)

// projectGeo: from a start point, project bearing° + range(m) → new lat/lon.
func projectGeo(lat, lon, bearingDeg, rangeM float64) (float64, float64) {
	br := bearingDeg * math.Pi / 180
	lat1 := lat * math.Pi / 180
	lon1 := lon * math.Pi / 180
	d := rangeM / earthRadius
	lat2 := math.Asin(math.Sin(lat1)*math.Cos(d) +
		math.Cos(lat1)*math.Sin(d)*math.Cos(br))
	lon2 := lon1 + math.Atan2(
		math.Sin(br)*math.Sin(d)*math.Cos(lat1),
		math.Cos(d)-math.Sin(lat1)*math.Sin(lat2))
	return lat2 * 180 / math.Pi, lon2 * 180 / math.Pi
}

// offsetGeo: shift a point by (east, north) metres.
func offsetGeo(lat, lon, eastM, northM float64) (float64, float64) {
	dLat := northM / earthRadius
	dLon := eastM / (earthRadius * math.Cos(lat*math.Pi/180))
	return lat + dLat*180/math.Pi, lon + dLon*180/math.Pi
}

// bearingFromTo: forward azimuth (degrees, 0-360) from A to B.
func bearingFromTo(latA, lonA, latB, lonB float64) float64 {
	φ1 := latA * math.Pi / 180
	φ2 := latB * math.Pi / 180
	Δλ := (lonB - lonA) * math.Pi / 180
	y := math.Sin(Δλ) * math.Cos(φ2)
	x := math.Cos(φ1)*math.Sin(φ2) -
		math.Sin(φ1)*math.Cos(φ2)*math.Cos(Δλ)
	θ := math.Atan2(y, x) * 180 / math.Pi
	if θ < 0 {
		θ += 360
	}
	return θ
}

// metresBetween: signed (east, north) metres from A to B.
func metresBetween(latA, lonA, latB, lonB float64) (east, north float64) {
	dLat := (latB - latA) * math.Pi / 180
	dLon := (lonB - lonA) * math.Pi / 180
	north = dLat * earthRadius
	east = dLon * earthRadius * math.Cos(latA*math.Pi/180)
	return
}

// haversineMetres: great-circle distance in metres.
func haversineMetres(latA, lonA, latB, lonB float64) float64 {
	dLat := (latB - latA) * math.Pi / 180
	dLon := (lonB - lonA) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(latA*math.Pi/180)*math.Cos(latB*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadius * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// angleDiffDeg returns the signed shortest angular difference a-b in (-180,180].
func angleDiffDeg(a, b float64) float64 {
	d := math.Mod(a-b+540, 360) - 180
	return d
}
