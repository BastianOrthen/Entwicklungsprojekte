package main

import (
	"math"
	"strings"
	"time"
)

// DFObservation is the slimmed-down view of an MDS DF JSON line we need
// for tracking. It tolerates both the v1 flat layout and the v2 nested
// layout the Multi-Domain-Simulator emits.
type DFObservation struct {
	// v1 flat fields
	BearingID     string      `json:"bearingId,omitempty"`
	SensorName    string      `json:"sensorName,omitempty"`
	Geolocation   *latLonAlt  `json:"geolocation,omitempty"`
	Angle         float64     `json:"angle,omitempty"`         // °
	Uncertainty   float64     `json:"uncertainty,omitempty"`   // °, 1σ
	Range         float64     `json:"range,omitempty"`         // meters (max)
	TargetID      string      `json:"targetId,omitempty"`
	Timestamp     string      `json:"timestamp,omitempty"`
	Classifier    string      `json:"classifier,omitempty"`
	SensorModality string     `json:"sensorModality,omitempty"`
	HostPlatformID string     `json:"hostPlatformId,omitempty"`

	// v2 nested
	Sensor              *sensorBlock      `json:"sensor,omitempty"`
	Bearing             *bearingBlock     `json:"bearing,omitempty"`
	Emitter             *emitterBlock     `json:"emitter,omitempty"`
	HostPlatformIDV2    string            `json:"host_platform_id,omitempty"`
	TechnicalParameters *techBlock        `json:"technical_parameters,omitempty"`
	RangeMaxM           float64           `json:"range_max_m,omitempty"`
}

type latLonAlt struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
	Alt float64 `json:"alt,omitempty"`
}

type sensorBlock struct {
	ID  string  `json:"id,omitempty"`
	Lat float64 `json:"lat,omitempty"`
	Lon float64 `json:"lon,omitempty"`
	Alt float64 `json:"alt,omitempty"`
}

type bearingBlock struct {
	AngleDeg       float64 `json:"angle_deg,omitempty"`
	UncertaintyDeg float64 `json:"uncertainty_deg,omitempty"`
}

type emitterBlock struct {
	ID           string  `json:"id,omitempty"`
	PlatformID   string  `json:"platform_id,omitempty"`
	FrequencyMHz float64 `json:"frequency_mhz,omitempty"`
	Function     string  `json:"function,omitempty"`
	Name         string  `json:"name,omitempty"`
}

type techBlock struct {
	Frequency *struct {
		CenterMHz float64 `json:"center_mhz,omitempty"`
	} `json:"frequency,omitempty"`
}

// ── Derived accessors ───────────────────────────────────────────────────

// SensorPos returns the sensor station position. Second return is false
// when no usable sensor location is in the observation.
func (o *DFObservation) SensorPos() (lat, lon float64, ok bool) {
	switch {
	case o.Sensor != nil && (o.Sensor.Lat != 0 || o.Sensor.Lon != 0):
		return o.Sensor.Lat, o.Sensor.Lon, true
	case o.Geolocation != nil && (o.Geolocation.Lat != 0 || o.Geolocation.Lon != 0):
		return o.Geolocation.Lat, o.Geolocation.Lon, true
	}
	return 0, 0, false
}

// BearingDeg returns angle (deg) and 1σ uncertainty (deg). Second return
// is false when no bearing angle is present.
func (o *DFObservation) BearingDeg() (angle, sigma float64, ok bool) {
	if o.Bearing != nil {
		angle = o.Bearing.AngleDeg
		sigma = o.Bearing.UncertaintyDeg
		ok = true
	} else if o.Angle != 0 || o.Uncertainty != 0 {
		angle = o.Angle
		sigma = o.Uncertainty
		ok = true
	}
	if !ok {
		return 0, 0, false
	}
	angle = math.Mod(angle, 360)
	if angle < 0 {
		angle += 360
	}
	if sigma <= 0 {
		sigma = 1.0 // sensible floor — never zero
	}
	return angle, sigma, true
}

// MaxRangeM returns the sensor's max detection range in meters,
// falling back to defaultMax when the simulator emits none.
func (o *DFObservation) MaxRangeM(defaultMax float64) float64 {
	if o.RangeMaxM > 0 {
		return o.RangeMaxM
	}
	if o.Range > 0 {
		return o.Range
	}
	return defaultMax
}

// SensorID returns a human-readable sensor identifier.
func (o *DFObservation) SensorID() string {
	if o.Sensor != nil && o.Sensor.ID != "" {
		return o.Sensor.ID
	}
	return o.SensorName
}

// BearingTrackID returns the SAW-side track ID that mds-bridge will
// assign to a bearing for this same observation. Mirrors
// cmd/simulator/mds_bridge.go:platformTrackID — keep the priority
// order in sync. Used so the DF tracker can hand back the exact
// bearing IDs that contributed to a fused track.
func (o *DFObservation) BearingTrackID() string {
	if o.Emitter != nil && o.Emitter.PlatformID != "" {
		return "mds-plat-" + o.Emitter.PlatformID
	}
	if o.TargetID != "" {
		return "mds-plat-" + o.TargetID
	}
	if o.Emitter != nil && o.Emitter.ID != "" {
		return "mds-emit-" + o.Emitter.ID
	}
	if o.HostPlatformID != "" {
		return "mds-host-" + o.HostPlatformID
	}
	if o.HostPlatformIDV2 != "" {
		return "mds-host-" + o.HostPlatformIDV2
	}
	if o.BearingID != "" {
		return "mds-brg-" + o.BearingID
	}
	return ""
}

// PlatformKey returns a stable identity key for the platform behind the
// emitter, when MDS provides one. Empty when truly anonymous.
func (o *DFObservation) PlatformKey() string {
	if o.Emitter != nil && o.Emitter.PlatformID != "" {
		return o.Emitter.PlatformID
	}
	if o.TargetID != "" {
		return o.TargetID
	}
	if o.Emitter != nil && o.Emitter.ID != "" {
		return o.Emitter.ID
	}
	if o.HostPlatformID != "" {
		return o.HostPlatformID
	}
	return o.HostPlatformIDV2
}

// Source returns ELINT or COMINT based on sensor modality, or "" if unknown.
func (o *DFObservation) Source() string {
	switch strings.ToLower(o.SensorModality) {
	case "comint":
		return "COMINT"
	case "esm", "elint":
		return "ELINT"
	}
	return ""
}

// Domain maps the simulator's classifier hint to a SAW domain.
func (o *DFObservation) Domain() string {
	switch strings.ToLower(o.Classifier) {
	case "air", "aircraft", "fixedwing", "rotarywing", "drone", "uav":
		return "Air"
	case "sea", "surface", "ship", "vessel":
		return "SeaSurface"
	case "ground", "land", "vehicle":
		return "Ground"
	}
	return ""
}

// BattleDim is the tracker-internal motion class. It is finer-grained
// than the SAW Domain because we need it to pick the right motion-model
// mix for the IMM-PF: a stationary radar site and a manoeuvring vehicle
// both map to Domain "Ground" but want very different process noise.
type BattleDim uint8

const (
	DimUnknown    BattleDim = iota
	DimStationary           // fixed installations: radar sites, ground COMINT TX
	DimSemiMobile           // truck-mounted, slow ground vehicles, ships in port
	DimGround               // mobile ground (vehicles, mech inf, mobile SAM)
	DimSea                  // surface vessels under way
	DimAirRotary            // helicopters, hover-capable drones
	DimAirFixed             // fixed-wing aircraft, fast jets
)

// String makes log lines readable.
func (d BattleDim) String() string {
	switch d {
	case DimStationary:
		return "stationary"
	case DimSemiMobile:
		return "semimobile"
	case DimGround:
		return "ground"
	case DimSea:
		return "sea"
	case DimAirRotary:
		return "air-rotary"
	case DimAirFixed:
		return "air-fixed"
	}
	return "unknown"
}

// BattleDimension classifies the observation into one of the IMM-PF
// motion regimes. Combines Classifier ("air"/"ground"/…), Emitter.Name
// ("AN/TPS-77 surveillance radar" → stationary) and Emitter.Function
// ("surveillance"/"jammer"/"datalink") for richer hints.
func (o *DFObservation) BattleDimension() BattleDim {
	cls := strings.ToLower(o.Classifier)
	emitName := ""
	emitFn := ""
	platformID := ""
	if o.Emitter != nil {
		emitName = strings.ToLower(o.Emitter.Name)
		emitFn = strings.ToLower(o.Emitter.Function)
		platformID = strings.ToLower(o.Emitter.PlatformID)
	}

	// Strong stationary cues regardless of classifier.
	switch emitFn {
	case "surveillance", "early-warning", "early_warning",
		"acquisition", "ground-control", "ground_control",
		"jammer", "broadcast":
		return DimStationary
	}
	if strings.Contains(emitName, "surveillance") ||
		strings.Contains(emitName, "early-warning") ||
		strings.Contains(emitName, "early warning") {
		return DimStationary
	}

	// Platform-ID heuristics — the MDS simulator encodes the host
	// platform model in the ID (e.g. "su35-wing4", "frigate-pr11356-…",
	// "sa20-bat3", "btr80-coy7"). We treat these as ground-truth-grade
	// hints because they're emitted by the same simulator we're tracking.
	if d := dimFromPlatformID(platformID); d != DimUnknown {
		return d
	}

	// Air sub-classes.
	switch cls {
	case "rotarywing", "rotary-wing", "helicopter", "helo":
		return DimAirRotary
	case "drone", "uav":
		return DimAirRotary // most quad/hex drones; fast jets fall through
	case "fixedwing", "fixed-wing", "aircraft":
		return DimAirFixed
	case "air":
		// Generic "air" — guess from emitter name, else fixed-wing.
		if strings.Contains(emitName, "drone") ||
			strings.Contains(emitName, "uav") ||
			strings.Contains(emitName, "helo") {
			return DimAirRotary
		}
		return DimAirFixed
	}

	// Sea.
	switch cls {
	case "sea", "surface", "ship", "vessel":
		return DimSea
	}

	// Ground sub-classes — refine by emitter function.
	switch cls {
	case "ground", "land":
		return DimSemiMobile
	case "vehicle", "vehicles":
		return DimGround
	}

	return DimUnknown
}

// dimFromPlatformID matches the lower-cased platform_id against a
// curated list of model prefixes. The list intentionally errs on the
// side of fewer false positives — anything not matched returns
// DimUnknown so the higher-level classifier logic can take over.
func dimFromPlatformID(id string) BattleDim {
	if id == "" {
		return DimUnknown
	}
	// Helper: check any prefix.
	hasPrefix := func(prefixes ...string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(id, p) {
				return true
			}
		}
		return false
	}
	hasContains := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(id, s) {
				return true
			}
		}
		return false
	}

	// ── Fixed-wing aircraft / fast jets ────────────────────────────────
	if hasPrefix(
		"su-", "su2", "su3", "su5", "su7", "su9", "su15", "su17", "su20",
		"su22", "su24", "su25", "su27", "su30", "su33", "su34", "su35", "su37", "su47", "su57",
		"mig", "tu-", "tu2", "tu9", "tu1", "tu14", "tu16", "tu22", "tu95", "tu142", "tu160",
		"il-", "il18", "il20", "il22", "il38", "il76", "il78", "il80",
		"yak", "an-", "an12", "an26", "an72", "an124",
		"f-1", "f-2", "f-4", "f-5", "f-8", "f-14", "f-15", "f-16", "f-18", "f-22", "f-35",
		"a-10", "b-1", "b-2", "b-52", "c-130", "c-17",
		"e-3", "e-7", "e-8", "p-3", "p-8",
		"rafale", "typhoon", "gripen", "tornado", "mirage",
	) {
		return DimAirFixed
	}

	// ── Helicopters / rotary ───────────────────────────────────────────
	if hasPrefix(
		"mi-", "mi8", "mi17", "mi24", "mi28", "mi35",
		"ka-", "ka27", "ka29", "ka50", "ka52",
		"ah-", "uh-", "ch-", "sh-", "oh-",
		"nh90", "tigre", "lynx", "wildcat", "merlin",
	) {
		return DimAirRotary
	}

	// ── Drones / UAVs ──────────────────────────────────────────────────
	if hasContains("drone", "uav", "ucav") ||
		hasPrefix("mq-", "rq-", "bayraktar", "shahed", "orion", "altius",
			"forpost", "okhotnik", "lancet") {
		return DimAirRotary
	}

	// ── Surface vessels ────────────────────────────────────────────────
	if hasContains("frigate", "destroyer", "corvette", "cruiser",
		"carrier", "ship", "vessel", "boat", "ddg", "ffg", "cv-",
		"cvn-", "lcs", "uss-", "rfs-", "hms-") ||
		hasPrefix("pr.", "pr1", "pr2") {
		return DimSea
	}

	// ── Mobile SAM / semi-mobile ground ────────────────────────────────
	// SAM systems and EW battalions typically displace every few hours
	// but spend most of their time stationary.
	if hasPrefix(
		"sa-", "sa1", "sa2", "sa3", "sa5", "sa6", "sa8", "sa10", "sa11",
		"sa12", "sa15", "sa17", "sa20", "sa21", "sa22", "sa23",
		"s-3", "s3", "s-4", "s4", "s-5", "s5", "patriot", "thaad",
		"buk", "tor", "pantsir", "tunguska",
	) {
		return DimSemiMobile
	}

	// ── Mobile ground (tanks, APC, IFV) ────────────────────────────────
	if hasPrefix(
		"t-", "t6", "t7", "t8", "t9", "t14",
		"btr", "bmp", "bmd", "bdrm", "brdm",
		"m1", "m2", "m3", "leopard", "leclerc", "challenger", "abrams",
		"bradley", "stryker", "fuchs", "boxer", "puma", "marder",
	) {
		return DimGround
	}

	// ── Fixed installations (radar sites, jammers, comm sites) ─────────
	if hasContains("radar-station", "radarstation", "ews-", "ground-station",
		"site-") ||
		hasPrefix("gnd-", "ground-", "fixed-", "site-",
			"tps-", "fps-", "an-tps", "an-fps", "p-1", "p-3",
			"p-12", "p-14", "p-18", "p-19", "p-37", "p-40", "nebo",
			"gamma") {
		return DimStationary
	}

	return DimUnknown
}

// FrequencyHz returns the emitter centre frequency in Hz, or 0 if absent.
func (o *DFObservation) FrequencyHz() float64 {
	if o.TechnicalParameters != nil && o.TechnicalParameters.Frequency != nil &&
		o.TechnicalParameters.Frequency.CenterMHz > 0 {
		return o.TechnicalParameters.Frequency.CenterMHz * 1e6
	}
	if o.Emitter != nil && o.Emitter.FrequencyMHz > 0 {
		return o.Emitter.FrequencyMHz * 1e6
	}
	return 0
}

// ParsedTime decodes the observation timestamp; falls back to time.Now().
func (o *DFObservation) ParsedTime() time.Time {
	if o.Timestamp == "" {
		return time.Now()
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
	} {
		if t, err := time.Parse(layout, o.Timestamp); err == nil {
			return t
		}
	}
	return time.Now()
}
