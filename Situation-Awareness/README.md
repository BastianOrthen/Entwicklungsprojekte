# Situation-Awareness — Repository Workspace

This umbrella directory hosts the loosely coupled services that together provide the SAW (Situation Awareness) capability:

| Repo / Module | Purpose | Language | Key Ports |
|---|---|---|---|
| [SituationAwarenessMap/](SituationAwarenessMap) | API server, WebSocket hub, sensor ingest, REMP fanout, frontend | Go 1.24 + JS | HTTP `8080`, gRPC `50051`, alarm-fanout `50520`, REMP `50530`, REMP-overlay `50531` |
| [MHT/](MHT) | Multi-Hypothesis Tracker (Particle-Filter) consuming MDS DF, emitting localizations to SAW | Go 1.24 | HTTP `8090` (visualiser) |
| _(sibling)_ EW_Tasking | EW-Tasking REST gateway + microservices | Go 1.22 | HTTP `8080` (gateway), WS-Hub `8085` |

## Common conventions (post-2026-05 refactor)

All Go modules in this workspace follow the same MDS-derived foundation:

- `pkg/observability/` — `slog` setup, Prometheus metrics, OTel-ready
- `pkg/middleware/`    — composable HTTP middleware (`CORS`, `RequestLogger`, `Recover`)
- `pkg/health/`        — `/healthz` (liveness) + `/readyz` (registered checks)
- `Makefile`           — `build · run · test · vet · tidy · docker`
- `Dockerfile`         — multi-stage Alpine, `-ldflags="-s -w"`, version injected at build time
- env-driven `Config` struct loaded once at startup
- graceful shutdown via `signal.Notify` → `srv.Shutdown(ctx)`
- structured JSON or text logs (set `LOG_FORMAT=json` for production)

## Quick smoke

```powershell
# SAW
cd SituationAwarenessMap
make build
./bin/saw-api      # HTTP 8080, gRPC 50051

# MHT (requires SAW running for gRPC target)
cd ..\MHT
make build
./bin/mht -server localhost:50051 -mds-host localhost
```

## Endpoints (every service)

- `GET /healthz`  — liveness, returns service name + version + uptime
- `GET /readyz`   — runs registered readiness checks
- `GET /metrics`  — Prometheus exposition
