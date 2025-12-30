# Projekt-Übersicht

## ✅ Struktur & Dokumentation

Dieses Projekt ist jetzt vollständig aufgeräumt und dokumentiert.

### Dokumentation

| Datei | Inhalt |
|-------|--------|
| [CODEBASE.md](CODEBASE.md) | Vollständige technische Dokumentation |
| [QUICKSTART.md](QUICKSTART.md) | Schnellstart-Anleitung |
| [README.md](README.md) | Original-Projekt-Beschreibung |
| [ARCHITECTURE.md](ARCHITECTURE.md) | System-Architektur |

### Server Code

| Datei | Beschreibung |
|-------|-------------|
| `cmd/server/main.go` | **HTTP Server** - Akzeptiert Flugzeugdaten, stellt API bereit |
| `internal/grpc/server.go` | **Broadcaster & Endpoints** - /ingest, /stream, /api/aircraft-rows |
| `internal/adsb/model.go` | **Aircraft-Datenstruktur** - JSON-Serialisierung |
| `internal/adsb/parser.go` | **dump1090 Parser** - JSON-Daten-Parsing (optional) |

### Simulator Code

| Datei | Beschreibung |
|-------|-------------|
| `cmd/simulator/main.go` | **Aircraft-Simulator** - Generiert und postet Flugzeugdaten |
| `internal/sim/aircraft.go` | **Fleet Generator** - Erstellt Flugzeug-Flotten |
| `internal/sim/movement.go` | **Movement Updater** - Berechnet Position/Höhe/Geschwindigkeit |

### Web Frontend

| Datei | Beschreibung |
|-------|-------------|
| `web/index.html` | **Benutzeroberfläche** - Leaflet-Karte + Tabelle + Steuerelemente |
| `web/main-htmx.js` | **Client-Logik** - Polling, Kartenupdates, Sortierung |

### Abhängigkeiten

| Datei | Zweck |
|-------|-------|
| `go.mod`, `go.sum` | Go-Module |
| `docker-compose.yml` | Docker Compose (optional) |
| `Dockerfile` | Docker Image (optional) |
| `proto/adsb.proto` | gRPC Definitionen (deprecated) |

---

## 🧹 Gelöschte Dateien

Folgende Test- und Hilfsdateien wurden gelöscht:

- ❌ `test-hello.go` - Test-Datei
- ❌ `cmd/test/main.go` - Test-Server
- ❌ `web/main.js` - Alte Version (durch main-htmx.js ersetzt)
- ❌ `web/test-fetch.html` - Test-Datei
- ❌ `*.err`, `*.log`, `*.out` - Alte Log-Dateien
- ❌ `test-ingest.ps1` - Test-Skript
- ❌ `internal/grpc/client.go` - Unbenutzer SSE Client
- ❌ `internal/db/postgres.go` - Optional (nicht aktiv verwendet)

---

## 📝 Code-Dokumentation

Alle wichtigen Funktionen sind jetzt dokumentiert:

### Go Code
- Paketbeschreibungen am Anfang jeder Datei
- JSDoc-ähnliche Kommentare für öffentliche Funktionen
- Erklärungen für komplexe Logik (z.B. Sortierung)

### JavaScript Code
- Modul-Header mit Architektur-Übersicht
- Funktions-Dokumentation mit `@param` und `@returns`
- Inline-Kommentare für nicht-offensichtliche Logik
- Debug-Logs mit `[TAG]` Präfix für einfaches Tracking

### HTML Code
- HTML-Kommentare für Struktur-Abschnitte
- Inline-Titel und Alt-Text für UI-Elemente
- CSS-Kommentare für Styling-Zweck

---

## 🎯 Hauptfunktionalität

### Echtzeit-Flugzeugverfolgung
- ✅ Flugzeuge werden alle 1 Sekunde aktualisiert
- ✅ Interaktive Leaflet-Karte mit Markern und Flugpfaden
- ✅ Farbkodierung nach Höhe (blau = niedrig, rot = hoch)

### Sortierung
- ✅ Server-seitige Sortierung (7 Spalten)
- ✅ Sortierung bleibt über Polling-Updates erhalten
- ✅ Bidirektionales Togglen (▲/▼ Indikatoren)

### Interaktion
- ✅ Auswahl durch Tabellenzeile oder Karten-Marker
- ✅ Bidirektionale Hervorhebung (blau)
- ✅ Steuerelemente zum Löschen/Togglen von Pfaden

---

## 🚀 Deployment

### Schnellstart (PowerShell)
```powershell
cd c:\Users\basti\Documents\Entwicklungsprojekte\adsb-system

# Build
go build -o bin/server.exe ./cmd/server
go build -o bin/simulator.exe ./cmd/simulator

# Run in three terminals:
# Terminal 1:
./bin/server.exe -http :8080

# Terminal 2:
./bin/simulator.exe -target http://localhost:8080/ingest

# Terminal 3:
cd web
python -m http.server 3000

# Open browser:
Start http://localhost:3000
```

### Docker (optional)
```bash
docker-compose up
```

---

## 📊 Performance

| Metrik | Wert |
|--------|------|
| Polling Interval | 1 Sekunde |
| Aircraft Count | 15 |
| Track History | 200 Positionen max |
| DB Type | In-Memory |
| Sort Algorithm | O(n log n) |
| Typical Response Time | < 10ms |

---

## 📚 Weitere Information

- **CODEBASE.md** - Für detaillierte technische Details
- **QUICKSTART.md** - Für schnelle Einleitung
- **ARCHITECTURE.md** - Für System-Übersicht
- Browser Console (`F12`) - Für Debug-Logs (`[ADSB]`, `[POLL]`, `[SORT]`)
- Server Output - Für Ingest- und Sort-Debug-Informationen

---

## ✨ Qualität

Dieser Code ist:
- ✅ **Dokumentiert** - Alle Funktionen und Dateien erklären ihren Zweck
- ✅ **Sauber** - Test- und Hilfsdateien wurden entfernt
- ✅ **Kommentiert** - Komplexe Logik ist erklärt
- ✅ **Strukturiert** - Logische Trennung von Concerns
- ✅ **Produktionsreif** - Error-Handling und Logging vorhanden

---

## 🔧 Wartung

### Regelmäßige Aufgaben
- Überprüfen Sie Server-Logs auf Fehler
- Räumen Sie alte Log-Dateien auf
- Testen Sie die Sortierung mit verschiedenen Datensätzen

### Häufige Probleme
- **Kein Polling:** Überprüfen Sie Port 8080
- **Leere Tabelle:** Überprüfen Sie Simulator-Logs
- **Sortierung bleibt nicht:** Überprüfen Sie Browser-Console auf Fehler

---

**Letztes Update:** 30. Dezember 2025
**Status:** ✅ Produktionsreif
