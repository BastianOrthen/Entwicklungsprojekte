package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/basti/adsb-system/internal/adsb"
	_ "github.com/lib/pq"
	"github.com/pkg/errors"
)

// Connect opens a connection to Postgres. dsn example: "postgres://user:pass@localhost:5432/adsb?sslmode=disable"
func Connect(dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, errors.Wrap(err, "open postgres")
	}
	db.SetConnMaxLifetime(time.Minute * 5)
	db.SetMaxOpenConns(5)
	if err := db.Ping(); err != nil {
		return nil, errors.Wrap(err, "ping postgres")
	}
	return db, nil
}

// EnsureSchema returns SQL for creating the tracks table.
func EnsureSchema(db *sql.DB) error {
	q := `
CREATE TABLE IF NOT EXISTS aircraft (
    icao TEXT PRIMARY KEY,
    lat DOUBLE PRECISION,
    lon DOUBLE PRECISION,
    alt INTEGER,
    speed INTEGER,
    seen TIMESTAMP WITH TIME ZONE
);
`
	_, err := db.Exec(q)
	return err
}

// UpsertAircraft inserts or updates the latest position for an aircraft.
func UpsertAircraft(ctx context.Context, db *sql.DB, a adsb.Aircraft) error {
	q := `INSERT INTO aircraft (icao, lat, lon, alt, speed, seen) VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (icao) DO UPDATE SET lat = EXCLUDED.lat, lon = EXCLUDED.lon, alt = EXCLUDED.alt, speed = EXCLUDED.speed, seen = EXCLUDED.seen;`
	_, err := db.ExecContext(ctx, q, a.ICAO, a.Latitude, a.Longitude, a.Altitude, a.Speed, a.Seen)
	if err != nil {
		return fmt.Errorf("upsert aircraft: %w", err)
	}
	return nil
}
