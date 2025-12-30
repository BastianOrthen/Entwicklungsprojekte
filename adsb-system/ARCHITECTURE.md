# Code Architecture

Das System ist in folgende Packages aufgeteilt:

## `/internal/adsb` - ADS-B Data Handling
- **model.go**: Datenstrukturen (Aircraft)
  - `Aircraft`: Vollständig dokumentierte Struktur mit allen Feldern
- **parser.go**: Parsing und Datenabruf
  - `RunDump1090Poll()`: Periodischer Abruf von dump1090 JSON Endpoint
  - `parseAircraft()`: Konvertiert Raw-Daten in Aircraft-Objekte
  - `parseInt()`: Sichere Typ-Konvertierung für verschiedene numerische Typen

## `/internal/db` - Database Layer
- **postgres.go**: PostgreSQL-Operationen
  - `Connect()`: Verbindungsaufbau mit Pooling
  - `EnsureSchema()`: Idempotente Schema-Erstellung
  - `UpsertAircraft()`: Einfügen oder Aktualisieren von Aircraft-Daten

## `/internal/grpc` - HTTP/SSE Server
- **server.go**: HTTP-Server mit Streaming
  - `Broadcaster`: In-Memory Pub/Sub System für Aircraft-Updates
  - `Subscribe()`: Client-Verbindung registrieren
  - `Broadcast()`: Update an alle Clients senden (nicht-blockierend)
  - `handleStream()`: SSE-Streaming Endpoint
  - `handleIngest()`: JSON POST Endpoint zum Empfangen von Aircraft-Daten
  - `handleDebug()`: Debug-Statistiken (aktive Clients)
  - `handleHealth()`: Health-Check Endpoint

### Kleine, spezialisierte Handler
Jeder Handler hat eine klare, einzelne Verantwortung:
- Wenig Code pro Funktion (< 20 Zeilen)
- Lesbar und wartbar
- Leicht zu testen

## `/internal/sim` - Aircraft Simulation
- **aircraft.go**: Fleet-Generierung
  - `AircraftGenerator`: Erstellt realistische zufällige Flugzeuge
  - `GenerateFleet()`: Erzeugt Zivilflugzeuge + Fighter Jets
  - `generateCivilian()`: Einzelnes Flugzeug mit zufälligen Werten
  - `randomICAO()`, `randomCallsign()`, `randomSquawk()`: Zufalls-Daten Generator
  
- **movement.go**: Flugbahn-Simulation
  - `MovementUpdater`: Verwaltet Position und Höhe von Flugzeugen
  - `UpdatePosition()`: Great-Circle-Berechnung für neue Position
  - `UpdateAltitude()`: Unterschiedliche Manöver für Fighter vs. zivile Flugzeuge
  - `destPoint()`: Haversine-Formel für genaue Entfernungsberechnung

## `/cmd/server` - Main Server Process
- **main.go**: Orchestrierung aller Komponenten
  - `Config`: Struktur für Konfigurationsparameter
  - `parseConfig()`: Flag- und Env-Var Parsing
  - `startDataForwarding()`: Datenpipeline (Fetch → Broadcast → Persist)

## `/cmd/simulator` - Aircraft Simulation Process
- **main.go**: Simulations-Loop
  - `Simulator`: Verwaltet Simulation und HTTP-Posts
  - `Run()`: Haupt-Schleife (1x pro Sekunde)
  - `updateAndPostAircraft()`: Aktualisiert und postet Flugzeuge
  - `postAircraft()`: Einzelnes Flugzeug zum Server senden

## Designprinzipien

### Separation of Concerns
- Jedes Package hat eine klare Verantwortung
- Keine zirkulären Abhängigkeiten
- Data Models (adsb.Aircraft) sind zentral

### Small Functions
- Handler-Funktionen: < 20 Zeilen
- Utility-Funktionen extrahiert und dokumentiert
- Leicht zu testen und zu verstehen

### Documentation
- Alle Packages beginnen mit Paket-Dokumentation
- Alle Export-Funktionen (Groß-buchstabe) sind dokumentiert
- GoDoc-konform: `// FunctionName description`

### Configuration
- Flag-basiert in main.go
- Umgebungsvariablen-Fallback (z.B. POSTGRES_DSN)
- `Config` Struct für Typ-Sicherheit

### Error Handling
- Explizites Error-Handling
- Sinnvolle Fehlermeldungen mit Kontext
- Graceful Degradation (DB optional)

## Datenfluss

```
┌──────────────────┐
│  Simulator (.exe)│  Generiert Flugzeugdaten
└────────┬─────────┘
         │ HTTP POST /ingest
         ▼
┌──────────────────┐
│  Server (.exe)   │  SSE Broadcaster + Ingest
├──────────────────┤
│ /stream ────────────→ Browser (EventSource)
│ /ingest ◄────────────  Simulator
│ /healthz
│ /debug
└────────┬─────────┘
         │ Optional
         ▼
    PostgreSQL
    (Persistence)
```

## Deployment

1. **Server**: `.\bin\server.exe -http :8080`
2. **Simulator**: `.\bin\simulator.exe -target http://localhost:8080/ingest`
3. **Web**: `cd web && python -m http.server 3000`

Mit PostgreSQL:
```powershell
$env:POSTGRES_DSN = "postgres://user:pass@localhost:5432/adsb?sslmode=disable"
.\bin\server.exe -http :8080
```
