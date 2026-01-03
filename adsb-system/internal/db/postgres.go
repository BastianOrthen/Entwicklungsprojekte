// Package db provides database operations for persisting aircraft tracking data.
// Currently supports PostgreSQL as the backend.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/basti/adsb-system/internal/adsb"
	_ "github.com/lib/pq"
	"github.com/pkg/errors"
)

// CanonicalSource maps various source labels to the three main sources
// requested by the UI: antenna, simulator, internet.
func CanonicalSource(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, "\"'")
	if s == "" {
		return "unknown"
	}
	switch s {
	case "dump1090", "readsb", "antenna", "rtl", "rtlsdr", "rtl-sdr":
		return "antenna"
	case "sim", "simulator":
		return "simulator"
	case "internet", "net", "api", "online":
		return "internet"
	default:
		return s
	}
}

// Connect opens a connection to PostgreSQL and validates it.
// dsn example: "postgres://user:pass@localhost:5432/adsb?sslmode=disable"
// Returns an error if the connection fails or the server is unreachable.
func Connect(dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, errors.Wrap(err, "open postgres")
	}
	db.SetConnMaxLifetime(time.Minute * 5)
	db.SetMaxOpenConns(5)

	// In Docker Compose setups, the database container can be started but not yet
	// resolvable/reachable (DNS entry or readiness). Retry briefly to avoid
	// disabling persistence due to a short race at startup.
	deadline := time.Now().Add(30 * time.Second)
	sleep := 200 * time.Millisecond
	for {
		if err := db.Ping(); err == nil {
			break
		} else {
			if time.Now().After(deadline) {
				return nil, errors.Wrap(err, "ping postgres")
			}
			time.Sleep(sleep)
			if sleep < 2*time.Second {
				sleep *= 2
				if sleep > 2*time.Second {
					sleep = 2 * time.Second
				}
			}
		}
	}
	return db, nil
}

// EnsureSchema creates the aircraft table if it doesn't exist.
// Safe to call multiple times - uses CREATE TABLE IF NOT EXISTS.
func EnsureSchema(db *sql.DB) error {
	stmts := []string{
		// Current state table (one row per ICAO)
		`
CREATE TABLE IF NOT EXISTS aircraft (
    icao TEXT PRIMARY KEY,
    lat DOUBLE PRECISION,
    lon DOUBLE PRECISION,
    alt INTEGER,
    speed INTEGER,
    heading INTEGER,
    callsign TEXT,
    squawk TEXT,
    rssi DOUBLE PRECISION,
    vertical_rate INTEGER,
    messages INTEGER,
    on_ground BOOLEAN,
    source TEXT,
    origin_country TEXT,
    geo_alt_ft INTEGER,
    baro_alt_ft INTEGER,
    velocity_ms DOUBLE PRECISION,
    seen TIMESTAMP WITH TIME ZONE
);
`,
		// Backfill columns for older installations
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS heading INTEGER;`,
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS callsign TEXT;`,
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS squawk TEXT;`,
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS rssi DOUBLE PRECISION;`,
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS vertical_rate INTEGER;`,
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS messages INTEGER;`,
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS on_ground BOOLEAN;`,
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS source TEXT;`,
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS origin_country TEXT;`,
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS geo_alt_ft INTEGER;`,
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS baro_alt_ft INTEGER;`,
		`ALTER TABLE aircraft ADD COLUMN IF NOT EXISTS velocity_ms DOUBLE PRECISION;`,

		// Historical table (append-only)
		`
CREATE TABLE IF NOT EXISTS aircraft_reports (
    id BIGSERIAL PRIMARY KEY,
    icao TEXT NOT NULL,
    source TEXT NOT NULL,
    lat DOUBLE PRECISION,
    lon DOUBLE PRECISION,
    alt INTEGER,
    speed INTEGER,
    heading INTEGER,
    callsign TEXT,
    squawk TEXT,
    rssi DOUBLE PRECISION,
    vertical_rate INTEGER,
    messages INTEGER,
    on_ground BOOLEAN,
    origin_country TEXT,
    geo_alt_ft INTEGER,
    baro_alt_ft INTEGER,
    velocity_ms DOUBLE PRECISION,
    seen TIMESTAMP WITH TIME ZONE NOT NULL
);
`,
		`CREATE INDEX IF NOT EXISTS idx_aircraft_reports_seen ON aircraft_reports(seen);`,
		`CREATE INDEX IF NOT EXISTS idx_aircraft_reports_source_seen ON aircraft_reports(source, seen);`,
		`CREATE INDEX IF NOT EXISTS idx_aircraft_reports_icao_seen ON aircraft_reports(icao, seen);`,
	}

	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	return nil
}

// UpsertAircraft inserts a new aircraft record or updates an existing one.
// If an aircraft with the same ICAO exists, its position, altitude, speed, and timestamp are updated.
// This is the primary way to persist real-time aircraft positions.
func UpsertAircraft(ctx context.Context, db *sql.DB, a adsb.Aircraft) error {
	src := CanonicalSource(a.Source)
	q := `INSERT INTO aircraft (
  icao, lat, lon, alt, speed, heading, callsign, squawk, rssi, vertical_rate, messages, on_ground,
  source, origin_country, geo_alt_ft, baro_alt_ft, velocity_ms, seen
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
  $13,$14,$15,$16,$17,$18
)
ON CONFLICT (icao) DO UPDATE SET
  lat=EXCLUDED.lat,
  lon=EXCLUDED.lon,
  alt=EXCLUDED.alt,
  speed=EXCLUDED.speed,
  heading=EXCLUDED.heading,
  callsign=EXCLUDED.callsign,
  squawk=EXCLUDED.squawk,
  rssi=EXCLUDED.rssi,
  vertical_rate=EXCLUDED.vertical_rate,
  messages=EXCLUDED.messages,
  on_ground=EXCLUDED.on_ground,
  source=EXCLUDED.source,
  origin_country=EXCLUDED.origin_country,
  geo_alt_ft=EXCLUDED.geo_alt_ft,
  baro_alt_ft=EXCLUDED.baro_alt_ft,
  velocity_ms=EXCLUDED.velocity_ms,
  seen=EXCLUDED.seen;`
	_, err := db.ExecContext(ctx, q,
		a.ICAO, a.Latitude, a.Longitude, a.Altitude, a.Speed, a.Heading, a.Callsign, a.Squawk, a.RSSI, a.VerticalRate, a.Messages, a.OnGround,
		src, a.Origin, a.GeoAlt, a.BaroAlt, a.Velocity, a.Seen,
	)
	if err != nil {
		return fmt.Errorf("upsert aircraft: %w", err)
	}
	return nil
}

// InsertAircraftReport appends a single ADS-B report to the historical table.
// This enables offline querying by source and time range.
func InsertAircraftReport(ctx context.Context, db *sql.DB, a adsb.Aircraft) error {
	src := CanonicalSource(a.Source)
	q := `INSERT INTO aircraft_reports (
  icao, source, lat, lon, alt, speed, heading, callsign, squawk, rssi, vertical_rate, messages, on_ground,
  origin_country, geo_alt_ft, baro_alt_ft, velocity_ms, seen
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,
  $14,$15,$16,$17,$18
);`
	_, err := db.ExecContext(ctx, q,
		a.ICAO, src, a.Latitude, a.Longitude, a.Altitude, a.Speed, a.Heading, a.Callsign, a.Squawk, a.RSSI, a.VerticalRate, a.Messages, a.OnGround,
		a.Origin, a.GeoAlt, a.BaroAlt, a.Velocity, a.Seen,
	)
	if err != nil {
		return fmt.Errorf("insert aircraft report: %w", err)
	}
	return nil
}

type HistoryQuery struct {
	Source string
	From   time.Time
	To     time.Time
	Limit  int
	// RequireFields filters rows where the given fields are present.
	// Supported keys (case-insensitive, underscores/camelCase tolerated):
	// callsign, squawk, origin_country/origin, alt, speed, heading, rssi,
	// vertical_rate/verticalRate, messages, on_ground/onGround,
	// geo_alt_ft/geoAltFt, baro_alt_ft/baroAltFt, velocity_ms/velocityMs.
	RequireFields []string
}

// QueryAircraftReports returns historical reports filtered by source and time window.
func QueryAircraftReports(ctx context.Context, db *sql.DB, q HistoryQuery) ([]adsb.Aircraft, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50000
	}
	if limit > 50000 {
		limit = 50000
	}

	src := strings.TrimSpace(q.Source)
	if src != "" {
		src = CanonicalSource(src)
	}

	// Build optional field presence filters (whitelisted)
	requireSet := map[string]bool{}
	for _, raw := range q.RequireFields {
		k := strings.TrimSpace(raw)
		if k == "" {
			continue
		}
		k = strings.ToLower(k)
		k = strings.ReplaceAll(k, "_", "")
		switch k {
		case "callsign":
			requireSet["callsign"] = true
		case "squawk":
			requireSet["squawk"] = true
		case "origin", "origincountry":
			requireSet["origin_country"] = true
		case "alt":
			requireSet["alt"] = true
		case "speed":
			requireSet["speed"] = true
		case "heading":
			requireSet["heading"] = true
		case "rssi":
			requireSet["rssi"] = true
		case "verticalrate":
			requireSet["vertical_rate"] = true
		case "messages":
			requireSet["messages"] = true
		case "onground":
			requireSet["on_ground"] = true
		case "geoaltft":
			requireSet["geo_alt_ft"] = true
		case "baroaltft":
			requireSet["baro_alt_ft"] = true
		case "velocityms":
			requireSet["velocity_ms"] = true
		}
	}

	where := []string{
		"seen >= $1 AND seen <= $2",
		"($3 = '' OR source = $3)",
	}
	for key := range requireSet {
		switch key {
		case "callsign":
			where = append(where, "callsign IS NOT NULL AND btrim(callsign) <> ''")
		case "squawk":
			where = append(where, "squawk IS NOT NULL AND btrim(squawk) <> ''")
		case "origin_country":
			where = append(where, "origin_country IS NOT NULL AND btrim(origin_country) <> ''")
		case "alt":
			where = append(where, "alt IS NOT NULL")
		case "speed":
			where = append(where, "speed IS NOT NULL")
		case "heading":
			where = append(where, "heading IS NOT NULL")
		case "rssi":
			where = append(where, "rssi IS NOT NULL")
		case "vertical_rate":
			where = append(where, "vertical_rate IS NOT NULL")
		case "messages":
			where = append(where, "messages IS NOT NULL")
		case "on_ground":
			where = append(where, "on_ground IS NOT NULL")
		case "geo_alt_ft":
			where = append(where, "geo_alt_ft IS NOT NULL")
		case "baro_alt_ft":
			where = append(where, "baro_alt_ft IS NOT NULL")
		case "velocity_ms":
			where = append(where, "velocity_ms IS NOT NULL")
		}
	}

	stmt := `
SELECT
  icao, lat, lon, alt, speed, heading, callsign, squawk, rssi, vertical_rate, messages, on_ground,
  source, origin_country, geo_alt_ft, baro_alt_ft, velocity_ms, seen
FROM aircraft_reports
WHERE ` + strings.Join(where, "\n  AND ") + `
ORDER BY seen ASC
LIMIT $4;`

	rows, err := db.QueryContext(ctx, stmt, q.From, q.To, src, limit)
	if err != nil {
		return nil, fmt.Errorf("query aircraft reports: %w", err)
	}
	defer rows.Close()

	var out []adsb.Aircraft
	for rows.Next() {
		var a adsb.Aircraft
		if err := rows.Scan(
			&a.ICAO, &a.Latitude, &a.Longitude, &a.Altitude, &a.Speed, &a.Heading, &a.Callsign, &a.Squawk, &a.RSSI, &a.VerticalRate, &a.Messages, &a.OnGround,
			&a.Source, &a.Origin, &a.GeoAlt, &a.BaroAlt, &a.Velocity, &a.Seen,
		); err != nil {
			return nil, fmt.Errorf("scan aircraft report: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate aircraft reports: %w", err)
	}
	return out, nil
}

// QueryLatestAircraftReports returns the latest report per ICAO filtered by source and time window.
// This is useful for large windows where returning every report would be too heavy for the UI.
func QueryLatestAircraftReports(ctx context.Context, db *sql.DB, q HistoryQuery) ([]adsb.Aircraft, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 5000
	}
	if limit > 20000 {
		limit = 20000
	}

	src := strings.TrimSpace(q.Source)
	if src != "" {
		src = CanonicalSource(src)
	}

	// Build optional field presence filters (whitelisted)
	requireSet := map[string]bool{}
	for _, raw := range q.RequireFields {
		k := strings.TrimSpace(raw)
		if k == "" {
			continue
		}
		k = strings.ToLower(k)
		k = strings.ReplaceAll(k, "_", "")
		switch k {
		case "callsign":
			requireSet["callsign"] = true
		case "squawk":
			requireSet["squawk"] = true
		case "origin", "origincountry":
			requireSet["origin_country"] = true
		case "alt":
			requireSet["alt"] = true
		case "speed":
			requireSet["speed"] = true
		case "heading":
			requireSet["heading"] = true
		case "rssi":
			requireSet["rssi"] = true
		case "verticalrate":
			requireSet["vertical_rate"] = true
		case "messages":
			requireSet["messages"] = true
		case "onground":
			requireSet["on_ground"] = true
		case "geoaltft":
			requireSet["geo_alt_ft"] = true
		case "baroaltft":
			requireSet["baro_alt_ft"] = true
		case "velocityms":
			requireSet["velocity_ms"] = true
		}
	}

	where := []string{
		"seen >= $1 AND seen <= $2",
		"($3 = '' OR source = $3)",
	}
	for key := range requireSet {
		switch key {
		case "callsign":
			where = append(where, "callsign IS NOT NULL AND btrim(callsign) <> ''")
		case "squawk":
			where = append(where, "squawk IS NOT NULL AND btrim(squawk) <> ''")
		case "origin_country":
			where = append(where, "origin_country IS NOT NULL AND btrim(origin_country) <> ''")
		case "alt":
			where = append(where, "alt IS NOT NULL")
		case "speed":
			where = append(where, "speed IS NOT NULL")
		case "heading":
			where = append(where, "heading IS NOT NULL")
		case "rssi":
			where = append(where, "rssi IS NOT NULL")
		case "vertical_rate":
			where = append(where, "vertical_rate IS NOT NULL")
		case "messages":
			where = append(where, "messages IS NOT NULL")
		case "on_ground":
			where = append(where, "on_ground IS NOT NULL")
		case "geo_alt_ft":
			where = append(where, "geo_alt_ft IS NOT NULL")
		case "baro_alt_ft":
			where = append(where, "baro_alt_ft IS NOT NULL")
		case "velocity_ms":
			where = append(where, "velocity_ms IS NOT NULL")
		}
	}

	stmt := `
SELECT DISTINCT ON (icao)
  icao, lat, lon, alt, speed, heading, callsign, squawk, rssi, vertical_rate, messages, on_ground,
  source, origin_country, geo_alt_ft, baro_alt_ft, velocity_ms, seen
FROM aircraft_reports
WHERE ` + strings.Join(where, "\n  AND ") + `
ORDER BY icao, seen DESC
LIMIT $4;`

	rows, err := db.QueryContext(ctx, stmt, q.From, q.To, src, limit)
	if err != nil {
		return nil, fmt.Errorf("query latest aircraft reports: %w", err)
	}
	defer rows.Close()

	var out []adsb.Aircraft
	for rows.Next() {
		var a adsb.Aircraft
		if err := rows.Scan(
			&a.ICAO, &a.Latitude, &a.Longitude, &a.Altitude, &a.Speed, &a.Heading, &a.Callsign, &a.Squawk, &a.RSSI, &a.VerticalRate, &a.Messages, &a.OnGround,
			&a.Source, &a.Origin, &a.GeoAlt, &a.BaroAlt, &a.Velocity, &a.Seen,
		); err != nil {
			return nil, fmt.Errorf("scan latest aircraft report: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate latest aircraft reports: %w", err)
	}
	return out, nil
}
