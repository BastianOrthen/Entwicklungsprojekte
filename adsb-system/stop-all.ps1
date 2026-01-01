$ErrorActionPreference = 'SilentlyContinue'

Write-Host "[STOP] Killing server/simulator/web/internet..."

# Try graceful-ish by name first
Get-Process server, simulator, web, internet -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

# Fallback: kill by image name (covers spawned console instances)
taskkill /F /IM server.exe /IM simulator.exe /IM web.exe /IM internet.exe 2>$null | Out-Null

Write-Host "[STOP] Done."