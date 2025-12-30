# ADSB Tracking System - Codebase Documentation

A real-time aircraft tracking system with a web-based interface displaying aircraft positions on a Leaflet map with sortable data table.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│  WEB BROWSER (http://localhost:3000)                        │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ index.html + main-htmx.js                           │   │
│  │ - Leaflet map (full-screen)                         │   │
│  │ - Aircraft table with sortable columns              │   │
│  │ - Real-time polling (every 1 sec)                   │   │
│  └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
                           ↓ HTTP
                    (JSON, HTML rows)
┌─────────────────────────────────────────────────────────────┐
│  GO SERVER (port 8080)                                      │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ cmd/server/main.go + internal/grpc/server.go        │   │
│  │ - HTTP endpoints: /ingest, /stream, /api/aircraft*  │   │
│  │ - In-memory aircraft store (broadcaster)            │   │
│  │ - Server-side sorting logic                         │   │
│  │ - Real-time broadcasting to SSE clients             │   │
│  └─────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
     ↑           ↑                          ↑
     │           │                          │
   INGEST    STREAM (SSE)              QUERY RESULTS
     │           │                          │
┌────┴──┐  ┌─────┴────────┐         [api/aircraft-rows]
│ SIM   │  │ SSE Clients  │         [with sort params]
└───────┘  └──────────────┘
```

## Directory Structure

```
adsb-system/
├── cmd/                      # Executable entry points
│   ├── server/main.go        # ADSB tracking server
│   └── simulator/main.go     # Aircraft data simulator
├── internal/                 # Core application packages
│   ├── adsb/                 # Aircraft data model & parsing
│   │   ├── model.go          # Aircraft struct, JSON marshaling
│   │   └── parser.go         # dump1090 JSON parser
│   ├── grpc/                 # HTTP server (SSE/ingest/API)
│   │   └── server.go         # Broadcaster, endpoints, sorting
│   ├── sim/                  # Aircraft simulation
│   │   ├── aircraft.go       # Fleet generation
│   │   └── movement.go       # Position/altitude updates
│   └── db/                   # Database (optional, unused)
│       └── postgres.go       # PostgreSQL connection
└── web/                      # Web frontend
    ├── index.html            # Main page (map + table)
    └── main-htmx.js          # Client-side map & polling logic
```

## Core Components

### 1. Server (`cmd/server/main.go` + `internal/grpc/server.go`)

**Purpose:** Accept aircraft data, store in memory, serve via HTTP API

**Endpoints:**
- `POST /ingest` - Accept JSON aircraft updates
- `GET /stream` - Server-Sent Events (SSE) real-time stream
- `GET /api/aircraft` - JSON list of all aircraft
- `GET /api/aircraft-rows` - HTML table rows (sortable)
- `GET /healthz` - Health check

**Key Features:**
- In-memory aircraft store (map[ICAO]Aircraft)
- Thread-safe broadcaster pattern for SSE
- Server-side sorting by 7 columns (id, callsign, alt, spd, hdg, squawk, rssi)
- CORS headers for browser clients

**Request/Response Example:**
```
GET http://localhost:8080/api/aircraft-rows?sort=alt&asc=true

Response: HTML table rows
<tr id="aircraft-A1E04C" data-lat="47.03" data-lon="9.80">
  <td>A1E04C</td>
  <td>GO992</td>
  <td>29061</td>
  ...
</tr>
```

### 2. Simulator (`cmd/simulator/main.go`)

**Purpose:** Generate realistic aircraft movement and POST to server

**Features:**
- 15 random civilian aircraft + 3 fighter jets
- Movement updater with realistic flight paths
- Posts JSON every 1 second to /ingest endpoint
- Logs: `[SIM] Posted A1E04C at 47.03,9.80 alt=29061 hdg=158 spd=491`

**Usage:**
```bash
./simulator -target http://localhost:8080/ingest
```

### 3. Web Frontend (`web/index.html` + `web/main-htmx.js`)

**Page Structure:**
- Full-screen Leaflet map (OpenStreetMap)
- Fixed overlay panel (bottom-left) with:
  - Aircraft table (8 columns)
  - Control buttons (Clear, Paths, Hide)
  - Help text

**JavaScript Client:**
- Polls `/api/aircraft-rows` every 1 second with sort parameters
- Parses HTML rows and updates map markers/polylines
- Altitude-based color coding (blue low → red high)
- Bidirectional selection (table ↔ map)
- Sortable columns with visual indicators (▲/▼)

**Polling Mechanism:**
```javascript
// Fetch with current sort params
const sortUrl = `http://localhost:8080/api/aircraft-rows?sort=alt&asc=true`;
fetch(sortUrl)
  .then(response => response.text())
  .then(html => {
    tbody.innerHTML = html;  // Update table
    triggerHTMXAfterSettle(); // Update map
  });
```

## Data Models

### Aircraft Structure

```go
type Aircraft struct {
  ICAO         string    // 24-bit hex identifier
  Latitude     float64   // WGS84 decimal degrees
  Longitude    float64   // WGS84 decimal degrees
  Altitude     int       // Feet
  Speed        int       // Knots
  Heading      int       // Degrees (0-360)
  Callsign     string    // Flight identifier (e.g., "LH123")
  Squawk       string    // Transponder code (octal)
  RSSI         float64   // Signal strength (dBm)
  Seen         time.Time // Last position report
  // ... additional optional fields
}
```

## Sorting Implementation

### Server-side (Go)

Located in `internal/grpc/server.go`, `handleAircraftRows` function:

1. Parse query parameters: `?sort=column&asc=true|false`
2. Lock aircraft map and create snapshot
3. Sort using `sort.Slice()` with comparators for each column
4. Generate HTML rows in sorted order
5. Return to client

```go
sortUrl := r.URL.Query().Get("sort")
ascending := r.URL.Query().Get("asc") == "true"
// Apply sort logic based on sortUrl
// Generate HTML with fmt.Fprintf(w, "<tr>...")
```

### Client-side (JavaScript)

Located in `web/main-htmx.js`:

1. Track current sort state: `currentSortKey`, `currentSortAsc`
2. On header click: `handleSort(column)`
3. Toggle direction if same column, else set ascending
4. Fetch with new URL: `/api/aircraft-rows?sort=column&asc=true|false`
5. Update sort indicators (▲/▼)
6. Every 1 second, polling uses current sort state

**Key Feature:** Sorting persists across polling updates because each fetch includes the sort parameters.

## Data Flow Examples

### Scenario 1: Aircraft Position Update

```
1. Simulator generates position
   └─ POST /ingest {"icao": "A1E04C", "lat": 47.03, ...}
   
2. Server receives & broadcasts
   └─ Stores in aircraft map
   └─ Sends to SSE clients
   
3. Browser polls /api/aircraft-rows
   └─ Server returns HTML rows (sorted if applicable)
   
4. JavaScript updates map
   └─ Creates/updates marker
   └─ Extends polyline trail
   └─ Updates table tbody
```

### Scenario 2: User Clicks "Alt" Column Header

```
1. Browser handles click → handleSort("alt")
   └─ currentSortKey = "alt"
   └─ currentSortAsc = true
   
2. Fetch /api/aircraft-rows?sort=alt&asc=true
   └─ Server sorts aircraft by altitude (ascending)
   └─ Returns HTML rows in altitude order
   
3. Browser updates table
   └─ tbody.innerHTML = html
   └─ Sort indicator shows ▲ on "Alt" header
   
4. Next polling (1 sec later)
   └─ Still uses ?sort=alt&asc=true
   └─ Sorting persists while data updates
   
5. User clicks "Alt" again → currentSortAsc = false
   └─ Next fetch uses ?sort=alt&asc=false
   └─ Reverse order, indicator changes to ▼
```

## Running the System

### Terminal 1: Start Server
```powershell
cd C:\Users\basti\Documents\Entwicklungsprojekte\adsb-system
go build -o bin/server.exe ./cmd/server
./bin/server.exe -http :8080
```

### Terminal 2: Start Simulator
```powershell
cd C:\Users\basti\Documents\Entwicklungsprojekte\adsb-system
go build -o bin/simulator.exe ./cmd/simulator
./bin/simulator.exe -target http://localhost:8080/ingest
```

### Terminal 3: Start Web Server
```powershell
cd C:\Users\basti\Documents\Entwicklungsprojekte\adsb-system\web
python -m http.server 3000
```

### Open Browser
```
http://localhost:3000
```

## Configuration

### Server Flags
```bash
-http ":8080"           # HTTP listen address
-dump "<url>"           # dump1090 JSON endpoint (optional)
-pg "<dsn>"             # PostgreSQL connection string (optional)
```

### Environment Variables
```bash
POSTGRES_DSN="..."      # Alternative to -pg flag
```

## Key Technical Decisions

1. **In-Memory Storage Over Database**
   - Rationale: Real-time performance for 1-second polling
   - Trade-off: Data lost on server restart
   
2. **Server-Side Sorting**
   - Rationale: Consistent ordering, handles large datasets
   - Alternative: Client-side sorting (rejected, causes flicker)
   
3. **JavaScript Polling Instead of HTMX**
   - Rationale: Full control over sort parameters in fetch
   - HTMX limitation: Cannot easily update query params on polling
   - Solution: Custom `setInterval()` with `fetch()` API
   
4. **HTML Table Rows Over JSON**
   - Rationale: Faster DOM updates (no JS parsing needed)
   - Server generates `<tr>` directly with data attributes
   
5. **Altitude-Based Coloring**
   - Gradient: blue (0ft) → red (45000ft)
   - Visual feedback for aircraft altitude at a glance

## Testing & Debugging

### Browser Console
- `[ADSB] ...` - Initialization messages
- `[POLL] ...` - Polling activity
- `[SORT] ...` - Sorting operations
- Click 'F12' to open DevTools

### Server Logs
- `[SERVER] Received aircraft: ...` - Ingest events
- `[SORT-DEBUG] ...` - Sort parameters & count
- `[SIM] Posted ...` - Simulator updates

### Manual API Testing
```powershell
# Get all aircraft
curl http://localhost:8080/api/aircraft

# Get sorted by altitude (ascending)
curl "http://localhost:8080/api/aircraft-rows?sort=alt&asc=true"

# Health check
curl http://localhost:8080/healthz
```

## Performance Characteristics

- **Polling Interval:** 1 second (configurable)
- **Aircraft Per Page:** ~20 visible rows
- **Database:** In-memory, fast
- **Sort Algorithm:** O(n log n) using Go's sort.Slice
- **Network:** One HTTP GET per second per browser

## Future Enhancements

- [ ] Filter/search by callsign or ICAO
- [ ] Zoom-to-aircraft feature
- [ ] Aircraft details panel (extended info)
- [ ] PostgreSQL persistence
- [ ] Real ADS-B receiver integration (dump1090)
- [ ] Multiple server instances with load balancing
- [ ] WebSocket instead of polling (lower latency)

## Dependencies

**Go:**
- Standard library (no external dependencies)
- Optional: github.com/lib/pq (PostgreSQL driver, unused)

**JavaScript:**
- Leaflet 1.9.4 (map rendering)
- No HTMX dependency (removed in favor of native fetch)

**HTML/CSS:**
- OpenStreetMap tiles (via HTTPS)
- Bootstrap not used (custom CSS)
