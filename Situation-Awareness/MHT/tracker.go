package main

import (
	"context"
	"log"
	"math"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// TrackerConfig is the runtime configuration of the MHT-PF tracker.
type TrackerConfig struct {
	NumParticles    int
	MinSensors      int     // minimum # distinct sensors to confirm a track
	GateChi2        float64 // not literally χ²; here the −2 ln(L) gating threshold
	MinRangeM       float64
	DefaultMaxRange float64
	ProcessNoiseM   float64
	VelocityNoiseM  float64
	EmitInterval    time.Duration
	TrackTTL        time.Duration
	MaxStdDevM      float64
	DivergeStdDevM  float64
	InstanceName    string

	// SmoothingAlpha is the EMA factor applied to emitted positions
	// (lat/lon, semi-axes, speed, course). 0 disables smoothing,
	// 1 emits raw PF means, anything in between trades responsiveness
	// for visual smoothness. ~0.30 is a good default for 1 Hz emit.
	SmoothingAlpha  float64

	// StationarySpeedThreshold (m/s) below which a confirmed track is
	// treated as stationary on emit: speed/course are forced to 0 and
	// position smoothing uses a tighter alpha. Avoids the static-radar
	// "creep" caused by residual velocity in the particle cloud.
	StationarySpeedThreshold float64
}

// Track is the state we keep per hypothesis.
type Track struct {
	ID         string
	PF         *ParticleFilter
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastEmit   time.Time
	NumUpdates int

	// Sensors that have contributed at least one observation.
	Sensors map[string]bool

	// Latest observation context — used purely for the visualiser.
	LastObservation *DFObservation
	LastBearingDeg  float64
	LastSensorLat   float64
	LastSensorLon   float64

	// Cached emitter metadata for the SAW Localization message.
	Source    string
	Domain    string
	Frequency float64 // Hz
	SignalType string
	PlatformKey string

	// Dim is the cached battle dimension. Set on track birth from the
	// first observation; sharpened later if a more specific
	// classification arrives (e.g. unknown → semimobile).
	Dim BattleDim

	// Contribs records the latest bearing snapshot per SAW bearing
	// TrackID that has fed this track. Sent to SAW as the
	// MergedLocating.contributing_bearings list so the operator can
	// see exactly which bearings were fused into the position.
	Contribs map[string]ContribBearing

	// Confirmed becomes true once min-sensors and stddev gates pass.
	Confirmed bool

	// EMA-smoothed output state. The raw weighted PF mean wobbles by
	// metres at every Predict because of stochastic resampling and
	// process noise — even when the underlying emitter is rock still.
	// We low-pass the *emitted* position so SAW shows a smooth track
	// while the filter cloud itself remains free to react quickly.
	emaInit       bool
	emaLat        float64
	emaLon        float64
	emaSemiMajor  float64
	emaSemiMinor  float64
	emaSpeedMps   float64
	emaCourseDeg  float64
}

// ContribBearing is a minimal bearing snapshot the tracker keeps so the
// SAW MergedLocating message can list every bearing that contributed
// to a track. The TrackID matches what mds-bridge sends to SAW.
type ContribBearing struct {
	TrackID    string
	Sensor     string
	Source     string
	Domain     string
	OriginLat  float64
	OriginLon  float64
	Direction  float64 // deg
	ErrorDeg   float64 // 1σ
	MaxRange   float64 // m
	Frequency  float64 // Hz
	SignalType string
	Timestamp  time.Time
}

// Tracker is the multi-hypothesis tracker. It owns all live tracks and
// the goroutine that periodically predicts + prunes.
type Tracker struct {
	cfg TrackerConfig
	out *sawOutput
	rng *rand.Rand

	mu     sync.RWMutex
	tracks map[string]*Track // id → track

	// Selected track for the visualiser; protected by selMu.
	selMu      sync.RWMutex
	selectedID string

	// Lightweight statistics for the operator log.
	statsMu  sync.Mutex
	statsObs int
	statsTs  time.Time
	emitLogTs time.Time

	// snapshot holds the latest read-only view of all tracks; refreshed
	// from the predict-and-prune goroutine. API handlers read from this
	// instead of t.mu so high-rate observations cannot starve them.
	snapshot atomic.Value // *trackerSnapshot
}

// trackerSnapshot is an immutable, JSON-ready picture of every live track.
type trackerSnapshot struct {
	Summaries []TrackSummary
	Details   map[string]*TrackDetail
}

func NewTracker(cfg TrackerConfig, out *sawOutput) *Tracker {
	return &Tracker{
		cfg:     cfg,
		out:     out,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
		tracks:  make(map[string]*Track),
		statsTs: time.Now(),
	}
}

// Run drives the periodic predict + prune loop until ctx is cancelled.
// Observations come in via OnObservation from the input goroutine.
func (t *Tracker) Run(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("df-tracker: Run goroutine PANIC: %v — restarting in 1s", r)
			time.Sleep(1 * time.Second)
			go t.Run(ctx)
		}
	}()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	var lastBeat time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if now.Sub(lastBeat) >= 5*time.Second {
				lastBeat = now
				log.Printf("df-tracker: Run alive, %d tracks", len(t.tracks))
			}
			t.predictAndPrune(now)
			t.maybeEmit(now)
			t.refreshSnapshot()
		}
	}
}

// OnObservation is the entry point for every parsed DF observation.
// It performs association, predict-update on the winner, and creates
// a new hypothesis if no existing track explains the observation.
func (t *Tracker) OnObservation(obs *DFObservation) {
	sensorLat, sensorLon, ok := obs.SensorPos()
	if !ok {
		return
	}
	bearingDeg, sigmaDeg, ok := obs.BearingDeg()
	if !ok {
		return
	}
	sensorID := obs.SensorID()
	if sensorID == "" {
		sensorID = "unknown"
	}
	maxRange := obs.MaxRangeM(t.cfg.DefaultMaxRange)
	now := obs.ParsedTime()
	if now.IsZero() {
		now = time.Now()
	}
	platformKey := obs.PlatformKey()

	t.mu.Lock()
	defer t.mu.Unlock()

	// 1) Predict every track up to "now" so association uses current state.
	for _, tr := range t.tracks {
		dt := now.Sub(tr.UpdatedAt).Seconds()
		if dt > 0 && dt < 5*60 {
			tr.PF.Predict(dt, t.cfg.ProcessNoiseM, t.cfg.VelocityNoiseM)
		}
	}

	// 2) Score association against every track.
	type cand struct {
		tr  *Track
		ll  float64 // observation likelihood
		bias float64 // platform-key matching bonus
	}
	obsFreq := obs.FrequencyHz()
	var cands []cand
	for _, tr := range t.tracks {
		ll := tr.PF.ObservationLikelihood(sensorLat, sensorLon, bearingDeg, sigmaDeg)
		bias := 1.0
		if platformKey != "" && tr.PlatformKey != "" {
			if platformKey == tr.PlatformKey {
				bias = 1e6 // heavy preference: same MDS platform → same hypothesis
			} else {
				bias = 1e-3 // strongly disprefer mixing two known platforms
			}
		}
		// Classification gate: if both observation and track carry a
		// frequency, penalise mismatches. A 1 % delta is treated as a
		// soft match; >5 % is treated as almost certainly a different
		// emitter (drops the candidate by 1e‑3 — well below the gate).
		// Without this cross‑emitter bearings whose lines happen to
		// intersect spatially get fused into ghost tracks that drift
		// across the map.
		if obsFreq > 0 && tr.Frequency > 0 {
			rel := math.Abs(obsFreq-tr.Frequency) / tr.Frequency
			switch {
			case rel <= 0.01:
				bias *= 1e3 // strong same‑emitter bonus
			case rel <= 0.05:
				bias *= 1.0 // ambiguous, neutral
			default:
				bias *= 1e-3 // different emitter
			}
		}
		cands = append(cands, cand{tr: tr, ll: ll, bias: bias})
	}
	sort.Slice(cands, func(i, j int) bool {
		return cands[i].ll*cands[i].bias > cands[j].ll*cands[j].bias
	})

	// Acceptance threshold derived from gating: −2 ln(L) ≤ χ². With L
	// being a sum of e^(−½(diff/σ)²) terms, accept when L ≥ exp(−χ²/2).
	threshold := math.Exp(-t.cfg.GateChi2 / 2)

	var assigned *Track
	if len(cands) > 0 {
		best := cands[0]
		if best.ll*best.bias >= threshold {
			assigned = best.tr
		}
	}

	if assigned == nil {
		// 3a) New hypothesis from this bearing.
		id := "pf-" + uuid.NewString()[:8]
		dim := obs.BattleDimension()
		pf := NewParticleFilterFromBearing(
			t.cfg.NumParticles,
			sensorLat, sensorLon, bearingDeg, math.Max(sigmaDeg, 0.5),
			t.cfg.MinRangeM, maxRange, dim, t.rng)
		assigned = &Track{
			ID:         id,
			PF:         pf,
			CreatedAt:  now,
			UpdatedAt:  now,
			Sensors:    map[string]bool{},
			PlatformKey: platformKey,
			Contribs:   map[string]ContribBearing{},
			Dim:        dim,
		}
		t.tracks[id] = assigned
		log.Printf("track %s: NEW (sensor=%s, brg=%.1f°, σ=%.1f°, dim=%s)",
			id, sensorID, bearingDeg, sigmaDeg, dim)

		// Auto-select the newest track for the visualiser when nothing
		// else is selected, so the operator sees activity immediately.
		t.selMu.Lock()
		if t.selectedID == "" {
			t.selectedID = id
		}
		t.selMu.Unlock()
	} else {
		// 3b) Update existing hypothesis.
		assigned.PF.Update(sensorLat, sensorLon, bearingDeg, sigmaDeg)
	}

	// 4) Refresh metadata and confirmation gating.
	assigned.UpdatedAt = now
	assigned.NumUpdates++
	assigned.Sensors[sensorID] = true
	assigned.LastObservation = obs
	assigned.LastBearingDeg = bearingDeg
	assigned.LastSensorLat = sensorLat
	assigned.LastSensorLon = sensorLon
	if obs.Source() != "" {
		assigned.Source = obs.Source()
	}
	if obs.Domain() != "" {
		assigned.Domain = obs.Domain()
	}
	if obs.FrequencyHz() > 0 {
		assigned.Frequency = obs.FrequencyHz()
	}
	if assigned.PlatformKey == "" && platformKey != "" {
		assigned.PlatformKey = platformKey
	}
	if obs.Emitter != nil && obs.Emitter.Name != "" {
		assigned.SignalType = obs.Emitter.Name
	}
	// Sharpen the cached battle dimension only when we get a
	// more-specific hint than what we already have. Never downgrade
	// from a known dim to Unknown.
	if d := obs.BattleDimension(); d != DimUnknown && assigned.Dim == DimUnknown {
		assigned.Dim = d
		assigned.PF.Dim = d
	}

	// Record this bearing as a contributing measurement so SAW can
	// later mark it "merged into" the resulting fused track. Keyed on
	// the bearing TrackID mds-bridge would assign — same key the
	// operator sees on the bearing in the SAW UI.
	if assigned.Contribs == nil {
		assigned.Contribs = map[string]ContribBearing{}
	}
	if btid := obs.BearingTrackID(); btid != "" {
		assigned.Contribs[btid] = ContribBearing{
			TrackID:    btid,
			Sensor:     sensorID,
			Source:     obs.Source(),
			Domain:     obs.Domain(),
			OriginLat:  sensorLat,
			OriginLon:  sensorLon,
			Direction:  bearingDeg,
			ErrorDeg:   sigmaDeg,
			MaxRange:   maxRange,
			Frequency:  obs.FrequencyHz(),
			SignalType: assigned.SignalType,
			Timestamp:  now,
		}
	}

	// Stats throttle.
	t.statsMu.Lock()
	t.statsObs++
	if dt := time.Since(t.statsTs); dt >= 5*time.Second {
		// We're already inside t.mu.Lock() — read the map size directly
		// without taking RLock(), which would deadlock on the same
		// RWMutex (writer holding the lock cannot take RLock).
		nT := len(t.tracks)
		log.Printf("df-tracker stats: %.1f obs/s, %d tracks, %d updates this window",
			float64(t.statsObs)/dt.Seconds(), nT, t.statsObs)
		t.statsObs = 0
		t.statsTs = time.Now()
	}
	t.statsMu.Unlock()
}

// predictAndPrune advances all particles to "now" without measurements,
// evicts stale tracks, and kills diverging clouds.
func (t *Tracker) predictAndPrune(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, tr := range t.tracks {
		dt := now.Sub(tr.UpdatedAt).Seconds()
		if dt > t.cfg.TrackTTL.Seconds() {
			log.Printf("track %s: EXPIRED (idle %.0fs)", id, dt)
			delete(t.tracks, id)
			continue
		}
		if dt > 0 {
			tr.PF.Predict(dt, t.cfg.ProcessNoiseM, t.cfg.VelocityNoiseM)
			tr.UpdatedAt = now // keep timeline consistent so next Predict uses small dt
		}
		// Diverged?
		_, _, _, _, sE, sN := tr.PF.MeanState()
		if math.Hypot(sE, sN) > t.cfg.DivergeStdDevM {
			log.Printf("track %s: DIVERGED (σ=%.0f m), pruned", id, math.Hypot(sE, sN))
			delete(t.tracks, id)
		}
	}
}

// maybeEmit pushes a localisation to SAW for every track that passes
// the confirmation gates and hasn't been emitted recently.
func (t *Tracker) maybeEmit(now time.Time) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var skipSensors, skipStd, skipInterval, emitted int
	for _, tr := range t.tracks {
		if len(tr.Sensors) < t.cfg.MinSensors {
			skipSensors++
			continue
		}
		lat, lon, vx, vy, sE, sN := tr.PF.MeanState()
		std := math.Hypot(sE, sN)
		if std > t.cfg.MaxStdDevM {
			skipStd++
			continue
		}
		if !tr.Confirmed {
			tr.Confirmed = true
			log.Printf("track %s: CONFIRMED  (σ=%.0f m, sensors=%d)",
				tr.ID, std, len(tr.Sensors))
		}
		if !tr.LastEmit.IsZero() && now.Sub(tr.LastEmit) < t.cfg.EmitInterval {
			skipInterval++
			continue
		}

		// ── Output stage: derive speed/course, decide stationary, smooth.
		speed := math.Hypot(vx, vy)
		course := 0.0
		if speed > 0.1 {
			// 0° = North, 90° = East (geographic course-over-ground).
			course = math.Mod(math.Atan2(vx, vy)*180/math.Pi+360, 360)
		}
		// Stationary lock — only meaningful for dimensions that
		// genuinely include a stationary regime. Fixed-wing aircraft
		// can never be stationary; treating their slow-down as static
		// would freeze the track during a turn through the pole. Air
		// rotary keeps the lock because helicopters/multirotor drones
		// do hover.
		stationaryEligible := true
		switch tr.Dim {
		case DimAirFixed, DimSea:
			stationaryEligible = false
		}
		stationary := stationaryEligible && tr.Confirmed &&
			speed < t.cfg.StationarySpeedThreshold
		if stationary {
			speed = 0
			course = 0
		}

		// Pick alpha. Stationary tracks smooth harder so the diamond
		// stops creeping; moving tracks use the configured alpha.
		alpha := t.cfg.SmoothingAlpha
		if alpha <= 0 {
			alpha = 1 // disabled → emit raw mean
		} else if alpha > 1 {
			alpha = 1
		}
		if stationary {
			alpha = math.Min(alpha, 0.10)
		}

		semiMajor := math.Max(sE, sN)
		semiMinor := math.Min(sE, sN)

		if !tr.emaInit {
			tr.emaInit = true
			tr.emaLat = lat
			tr.emaLon = lon
			tr.emaSemiMajor = semiMajor
			tr.emaSemiMinor = semiMinor
			tr.emaSpeedMps = speed
			tr.emaCourseDeg = course
		} else {
			tr.emaLat = alpha*lat + (1-alpha)*tr.emaLat
			tr.emaLon = alpha*lon + (1-alpha)*tr.emaLon
			tr.emaSemiMajor = alpha*semiMajor + (1-alpha)*tr.emaSemiMajor
			tr.emaSemiMinor = alpha*semiMinor + (1-alpha)*tr.emaSemiMinor
			tr.emaSpeedMps = alpha*speed + (1-alpha)*tr.emaSpeedMps
			// Course: shortest-arc EMA so 359° → 1° doesn't sweep
			// halfway around the compass.
			diff := angleDiffDeg(course, tr.emaCourseDeg)
			tr.emaCourseDeg = math.Mod(tr.emaCourseDeg+alpha*diff+360, 360)
		}

		tr.LastEmit = now
		emitted++
		contribs := make([]LocOutBearing, 0, len(tr.Contribs))
		for _, c := range tr.Contribs {
			contribs = append(contribs, LocOutBearing{
				TrackID:    c.TrackID,
				Sensor:     c.Sensor,
				Source:     c.Source,
				Domain:     c.Domain,
				OriginLat:  c.OriginLat,
				OriginLon:  c.OriginLon,
				Direction:  c.Direction,
				ErrorDeg:   c.ErrorDeg,
				MaxRange:   c.MaxRange,
				Frequency:  c.Frequency,
				SignalType: c.SignalType,
				Timestamp:  c.Timestamp,
			})
		}
		t.out.SendLocalization(LocalizationOut{
			TrackID:    tr.ID,
			Lat:        tr.emaLat,
			Lon:        tr.emaLon,
			Speed:      tr.emaSpeedMps,
			Azimuth:    tr.emaCourseDeg,
			SemiMajor:  tr.emaSemiMajor,
			SemiMinor:  tr.emaSemiMinor,
			Source:     tr.Source,
			Domain:     tr.Domain,
			Frequency:  tr.Frequency,
			Sensor:     t.cfg.InstanceName,
			SignalType: tr.SignalType,
			Timestamp:  now,
			ContributingBearings: contribs,
		})
	}
	// Periodic decision log so silent emit gaps become visible.
	if t.emitLogTs.IsZero() || now.Sub(t.emitLogTs) >= 5*time.Second {
		t.emitLogTs = now
		log.Printf("df-tracker: emit decisions — emitted=%d, skip(sensors=%d, std=%d, interval=%d), tracks=%d",
			emitted, skipSensors, skipStd, skipInterval, len(t.tracks))
	}
}

// ── Read-only accessors for the visualiser ─────────────────────────────

// TrackSummary is a JSON-serialisable view of a track for the API.
type TrackSummary struct {
	ID          string    `json:"id"`
	NumUpdates  int       `json:"numUpdates"`
	NumSensors  int       `json:"numSensors"`
	StdDevM     float64   `json:"stdDevM"`
	Confirmed   bool      `json:"confirmed"`
	Lat         float64   `json:"lat"`
	Lon         float64   `json:"lon"`
	UpdatedAt   time.Time `json:"updatedAt"`
	PlatformKey string    `json:"platformKey,omitempty"`
}

func (t *Tracker) ListTracks() []TrackSummary {
	if s, ok := t.snapshot.Load().(*trackerSnapshot); ok && s != nil {
		return s.Summaries
	}
	return nil
}

// TrackDetail bundles everything needed to draw one track in the
// visualiser: every particle, the weighted mean, and the most recent
// bearing line for context.
type TrackDetail struct {
	ID          string      `json:"id"`
	Confirmed   bool        `json:"confirmed"`
	NumSensors  int         `json:"numSensors"`
	StdDevM     float64     `json:"stdDevM"`
	UpdatedAt   time.Time   `json:"updatedAt"`
	Mean        []float64   `json:"mean"`        // [lat, lon]
	Velocity    []float64   `json:"velocity"`    // [vx_mps, vy_mps]
	Particles   [][]float64 `json:"particles"`   // [[lat, lon, weight]…]
	LastSensor  []float64   `json:"lastSensor"`  // [lat, lon]
	LastBearing float64     `json:"lastBearing"` // degrees
	PlatformKey string      `json:"platformKey,omitempty"`
	Source      string      `json:"source,omitempty"`
	Domain      string      `json:"domain,omitempty"`
}

func (t *Tracker) GetTrack(id string) (*TrackDetail, bool) {
	s, ok := t.snapshot.Load().(*trackerSnapshot)
	if !ok || s == nil {
		return nil, false
	}
	d, ok := s.Details[id]
	return d, ok
}

// ListAllDetails returns the full TrackDetail for every live track,
// ordered the same way as ListTracks (most-recently-updated first).
// It is lock-free — readers consume the immutable snapshot.
func (t *Tracker) ListAllDetails() []*TrackDetail {
	s, ok := t.snapshot.Load().(*trackerSnapshot)
	if !ok || s == nil {
		return nil
	}
	out := make([]*TrackDetail, 0, len(s.Summaries))
	for _, sm := range s.Summaries {
		if d, ok := s.Details[sm.ID]; ok {
			out = append(out, d)
		}
	}
	return out
}

// refreshSnapshot rebuilds the immutable read-only view of all tracks.
// It is invoked from the Run goroutine which already holds the timer
// cadence; API handlers consume the snapshot via atomic.Load with no
// contention against the high-rate OnObservation writer.
func (t *Tracker) refreshSnapshot() {
	t.mu.RLock()
	summ := make([]TrackSummary, 0, len(t.tracks))
	dets := make(map[string]*TrackDetail, len(t.tracks))
	for _, tr := range t.tracks {
		lat, lon, vx, vy, sE, sN := tr.PF.MeanState()
		std := safeFloat(math.Hypot(sE, sN))
		summ = append(summ, TrackSummary{
			ID:          tr.ID,
			NumUpdates:  tr.NumUpdates,
			NumSensors:  len(tr.Sensors),
			StdDevM:     std,
			Confirmed:   tr.Confirmed,
			Lat:         safeFloat(lat),
			Lon:         safeFloat(lon),
			UpdatedAt:   tr.UpdatedAt,
			PlatformKey: tr.PlatformKey,
		})
		parts := make([][]float64, len(tr.PF.Particles))
		for i, p := range tr.PF.Particles {
			parts[i] = []float64{safeFloat(p.Lat), safeFloat(p.Lon), safeFloat(p.Weight)}
		}
		dets[tr.ID] = &TrackDetail{
			ID:          tr.ID,
			Confirmed:   tr.Confirmed,
			NumSensors:  len(tr.Sensors),
			StdDevM:     std,
			UpdatedAt:   tr.UpdatedAt,
			Mean:        []float64{safeFloat(lat), safeFloat(lon)},
			Velocity:    []float64{safeFloat(vx), safeFloat(vy)},
			Particles:   parts,
			LastSensor:  []float64{safeFloat(tr.LastSensorLat), safeFloat(tr.LastSensorLon)},
			LastBearing: safeFloat(tr.LastBearingDeg),
			PlatformKey: tr.PlatformKey,
			Source:      tr.Source,
			Domain:      tr.Domain,
		}
	}
	t.mu.RUnlock()
	sort.Slice(summ, func(i, j int) bool {
		return summ[i].UpdatedAt.After(summ[j].UpdatedAt)
	})
	t.snapshot.Store(&trackerSnapshot{Summaries: summ, Details: dets})
}

// SetSelected stores the operator's chosen track for the visualiser.
// Empty string clears the selection.
func (t *Tracker) SetSelected(id string) bool {
	t.selMu.Lock()
	defer t.selMu.Unlock()
	if id == "" {
		t.selectedID = ""
		return true
	}
	t.mu.RLock()
	_, ok := t.tracks[id]
	t.mu.RUnlock()
	if !ok {
		return false
	}
	t.selectedID = id
	return true
}

func (t *Tracker) GetSelected() string {
	t.selMu.RLock()
	defer t.selMu.RUnlock()
	return t.selectedID
}

// safeFloat coerces NaN/±Inf to 0 so JSON encoding never fails. These
// values can briefly appear in degenerate particle clouds (e.g. Σw≈0
// just before a resample) and would otherwise abort the HTTP response
// with no body — surfacing as ERR_EMPTY_RESPONSE in the browser.
func safeFloat(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}
