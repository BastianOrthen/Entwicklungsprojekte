# Quick Start Guide

## Build & Run

### One-Command Start (PowerShell)

Start everything (server + simulator + web UI) with the included scripts:

```powershell
cd c:\Users\basti\Documents\Entwicklungsprojekte\adsb-system
./run-all.ps1
```

Stop everything:

```powershell
./stop-all.ps1
```

### One-Line Setup (PowerShell)

```powershell
cd c:\Users\basti\Documents\Entwicklungsprojekte\adsb-system
# Build
go build -o bin/server.exe ./cmd/server; go build -o bin/simulator.exe ./cmd/simulator; go build -o bin/web.exe ./cmd/web; Write-Host "✓ Build complete"

# Terminal 1: Server
Start-Process -FilePath ".\bin\server.exe" -ArgumentList "-http :8080" -NoNewWindow

# Terminal 2: Simulator
Start-Sleep -Seconds 1; Start-Process -FilePath ".\bin\simulator.exe" -ArgumentList "-target http://localhost:8080/ingest" -NoNewWindow

# Terminal 3: Web Server
Start-Sleep -Seconds 1; Start-Process -FilePath ".\bin\web.exe" -ArgumentList "-http :3000 -root .\\web" -NoNewWindow

# Open browser
Start-Sleep -Seconds 2; Start-Process "http://localhost:3000"
```

## What to Expect

1. **Map opens** with aircraft markers (colored dots)
2. **Table** shows 15 aircraft with columns:
   - ICAO (unique identifier)
   - Callsign (flight number)
   - Alt (altitude in feet)
   - Spd (speed in knots)
   - Hdg (heading in degrees)
   - SQK (transponder code)
   - RSSI (signal strength)
3. **Click table headers** to sort (▲/▼ indicators)
4. **Click markers** on map to highlight in table (blue)
5. **Flight paths** show as colored polylines (altitude-based)

## File Locations

| File | Purpose |
|------|---------|
| `cmd/server/main.go` | HTTP server, endpoints, sorting logic |
| `cmd/simulator/main.go` | Aircraft movement generator |
| `internal/grpc/server.go` | Broadcaster, request handlers |
| `internal/adsb/model.go` | Aircraft data structure |
| `internal/adsb/parser.go` | dump1090 JSON parsing |
| `internal/sim/aircraft.go` | Fleet generation |
| `internal/sim/movement.go` | Position/altitude calculations |
| `web/index.html` | UI (map, table, buttons) |
| `web/main-htmx.js` | Map rendering, polling, sorting |

## API Endpoints

```
GET  /api/aircraft           → JSON list of all aircraft
GET  /api/aircraft-rows      → HTML table rows (sortable)
GET  /api/aircraft-rows?sort=alt&asc=true   → Sorted rows
GET  /api/history            → JSON historical reports (Postgres)
POST /ingest                 → Accept aircraft JSON
GET  /stream                 → Server-Sent Events stream
GET  /healthz                → Health check (ok/error)
```

### History Query (PostgreSQL)

Wenn `POSTGRES_DSN` gesetzt ist (Docker-Compose macht das automatisch), speichert der Server:
- aktuellen Zustand pro Flugzeug in `aircraft`
- jede einzelne Meldung historisch in `aircraft_reports`

Die Web-UI hat im rechten Overlay einen Tab **Offline**:
- Quelle: Simulator | Internet
- Zeitraum: Von/Bis
- Filter: Feld muss vorhanden sein (z.B. Callsign/SQK/Origin)
- Button **Abfragen**: lädt die Ergebnisse aus Postgres, zeichnet sie auf die Karte und zeigt sie in der Offline-Tabelle.

API-Parameter für `/api/history`:
- `source`: `antenna` | `simulator` | `internet`
- `from`, `to`: RFC3339 (z.B. `2026-01-02T12:00:00Z`)
- `fields`: Komma-separiert, optional; wird als Filter interpretiert (Feld muss vorhanden sein), z.B. `callsign,squawk,origin_country`

## SDR / Antenne (readsb) via Docker

Das System kann ADS-B Daten direkt von einem SDR empfangen, indem `readsb` im Container läuft und der Server `aircraft.json` pollt.

- Start mit SDR-Compose-Override:
    - `docker compose -f docker-compose.yml -f docker-compose.sdr.yml up --build -d`

## Docker: Testen (inkl. Datenbank)

Start (Server + Postgres + Simulator + Web + optional Internet):

```powershell
cd c:\Users\basti\Documents\Entwicklungsprojekte\adsb-system
docker compose up --build -d
```

Dann öffnen:
- UI: `http://localhost:3000`

Verifizieren:
- Logs: `docker compose logs -f adsb-server`
- In der UI Tab **Offline** öffnen, Zeitraum setzen (z.B. letzte 10 Minuten) und **Abfragen**.

Hinweis (Windows): USB-Passthrough in Linux-Containern funktioniert typischerweise nur über WSL2 + `usbipd` (SDR an WSL/`docker-desktop` attachen). Auf nativen Linux-Systemen klappt es meist direkt.

## Sorting Columns

Click any column header to sort:
- **id** - ICAO 24-bit address (hex)
- **callsign** - Flight callsign (e.g., LH123)
- **alt** - Altitude in feet
- **spd** - Speed in knots
- **hdg** - Heading 0-360 degrees
- **squawk** - Transponder code (octal)
- **rssi** - Signal strength in dBm

Click again on same column to reverse sort direction.

## Map Colors

**Aircraft markers:** Hash-based colors (consistent per ICAO)

**Flight paths (polylines):**
- 🔵 Blue = Low altitude (< 11,000 ft)
- 🔷 Cyan = 11,000 - 22,500 ft
- 🟢 Green = 22,500 - 34,000 ft
- 🟡 Yellow = 34,000 - 39,000 ft
- 🔴 Red = High altitude (> 39,000 ft)

## Interactive Features

| Action | Effect |
|--------|--------|
| Click table row | Highlight in blue, show on map |
| Click map marker | Highlight row in blue |
| Click column header | Sort by that column (toggle A↔Z) |
| Click "Clear" button | Remove all flight paths |
| Click "Paths" button | Toggle flight path visibility |
| Click "Hide" button | Minimize table (show floating icon) |

## Troubleshooting

### "Connection refused" Error
- Check port 8080 is free: `netstat -ano | findstr :8080`
- Kill existing server: `taskkill /F /IM server.exe`

### Table shows 0 aircraft
- Check simulator is running
- Check logs for `[SIM] Posted` messages
- Refresh browser (F5)

### Sorting doesn't work
- Check browser console (F12) for errors
- Check server logs for `[SORT-DEBUG]` messages
- Verify URL has `?sort=column&asc=true|false`

### Map not showing
- Check Leaflet CDN is accessible (needs internet)
- Try different zoom level (scroll wheel)
- Check browser DevTools Network tab

## Performance Tips

- Set `hx-trigger="every 2s"` for less frequent polling (slower updates)
- Set `hx-trigger="every 500ms"` for faster polling (more server load)
- Disable polylines with "Paths" button for smoother performance
- Clear old tracks with "Clear" button to reduce memory

## Default Values

- **Server:** localhost:8080
- **Web:** localhost:3000
- **Polling interval:** 1 second
- **Aircraft count:** 15
- **Track history:** Last 200 positions
- **Map center:** Central Europe (50°, 8°)
- **Map zoom:** Level 6

## Development Commands

```bash
# Format code
go fmt ./cmd/... ./internal/...

# Run tests (if any)
go test ./cmd/... ./internal/...

# Build release binaries
go build -o bin/server.exe ./cmd/server
go build -o bin/simulator.exe ./cmd/simulator

# Check code quality
go vet ./cmd/... ./internal/...

# View dependencies
go mod graph
```

## Cleanup

```powershell
# Stop all services
taskkill /F /IM server.exe /IM simulator.exe /IM web.exe

# Remove binaries
rm bin/*.exe

# Clean Go build cache
go clean -cache
```

## Architecture Summary

```
Browser (Leaflet map + Table)
    ↓ fetch() every 1s
HTTP Server (port 8080)
    ↓ receives & sorts
In-Memory Aircraft Store
    ↑ polls
Simulator (posts every 1s)
```

**Data flow:** Simulator → Server → Browser (repeat every 1 second)

**Sorting:** Browser sends `?sort=column&asc=true`, Server sorts and returns HTML rows

**Selection:** Click table row or marker → Highlighted in blue (bidirectional)
