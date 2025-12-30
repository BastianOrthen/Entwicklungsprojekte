# ADSB System - Lokale Testanleitung

## Überblick

Das System besteht aus 3 Services:
1. **Server** (Port 8080): HTTP SSE Streamer + Ingest-Endpoint
2. **Simulator** (Posts zu Server): Generiert künstliche Flugzeugdaten
3. **Web** (Port 3000): Leaflet-Karte mit Echtzeit-Marker

## Alle Services starten

Öffne **3 separate PowerShell-Fenster**:

### Fenster 1: Server
```powershell
cd "C:\Users\basti\Documents\Entwicklungsprojekte\adsb-system"
& .\bin\server.exe -http :8080
```
Output sollte zeigen:
```
[SERVER] Starting on :8080
[SERVER] HTTP server started. Accepting /ingest and /stream requests.
[SERVER] Running. Press Ctrl+C to stop.
```

### Fenster 2: Simulator
```powershell
cd "C:\Users\basti\Documents\Entwicklungsprojekte\adsb-system"
& .\bin\simulator.exe -target http://localhost:8080/ingest
```
Output sollte zeigen:
```
Simulator starting, posting to: http://localhost:8080/ingest
[SIM] Posted ABC123 at 50.50,8.25 alt=12000
[SIM] Posted DEF456 at 51.20,7.80 alt=15000
...
```

### Fenster 3: Web-Server
```powershell
cd "C:\Users\basti\Documents\Entwicklungsprojekte\adsb-system\web"
python -m http.server 3000
```

## Test im Browser

1. Öffne: **http://localhost:3000**
2. Du solltest sehen:
   - Leaflet-Karte (zentriert auf [50,8], Zoom 6)
   - Dynamische Marker mit Flugzeugdaten (ICAO, Lat/Lon, Alt, Speed)
   - Marker werden jede Sekunde aktualisiert

## Debugging

### Testen ob Server läuft
```powershell
curl.exe http://localhost:8080/healthz
# Output: ok
```

### Testen wie viele Clients verbunden sind
```powershell
curl.exe http://localhost:8080/debug
# Output: Active subscribers: 1 (oder mehr)
```

### Manuell einen Flugzeug-POST senden
```powershell
$data = @{icao='MANUAL01'; lat=50.5; lon=8.5; alt=10000; speed=250; seen=(Get-Date).ToString("o")} | ConvertTo-Json
curl.exe -X POST http://localhost:8080/ingest -H "Content-Type: application/json" -d $data
```

## Browser-Konsole Debugging

Öffne im Browser F12 → Console, um Fehler zu sehen:
- `[ADSB]` Meldungen zeigen wenn der Stream verbunden ist
- Errors zeigen falls Verbindung fehlgeschlagen

## Bekannte Probleme & Lösungen

### "Kein Marker auf der Karte"
1. Server läuft nicht: Schau Fenster 1 auf Fehler
2. Simulator postet nicht: Schau Fenster 2 auf `[SIM] Posted` Logs
3. Browser verbindet sich nicht: F12 Console checken
4. Server-Logs checken: `[SERVER] Received aircraft: ...` sollte dort auftauchen

### Alle Services sofort beenden
```powershell
taskkill /F /IM server.exe /IM simulator.exe /IM python.exe
```

## Architektur

```
Simulator (generiert Daten)
    ↓ HTTP POST /ingest
Server (port 8080)
    ↓ SSE Stream /stream
Browser (port 3000)
    ↓ EventSource
Leaflet Map (zeigt Marker)
```

## Nächste Schritte

- Docker-Compose für einfaches Multi-Container Setup
- Echte Daten von RTL-SDR über dump1090
- Persistierung in Postgres
- Deployment auf Raspberry Pi

## Dokumentation

- **ARCHITECTURE.md**: Detaillierte Code-Architektur
- **REFACTORING.md**: Zusammenfassung der Code-Verbesserungen
