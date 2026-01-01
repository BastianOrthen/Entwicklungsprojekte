param(
  [int]$ServerPort = 8080,
  [int]$WebPort = 3000
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

Write-Host "[RUN] Building binaries..."
go build -o bin\server.exe .\cmd\server
if ($LASTEXITCODE -ne 0) { throw "server build failed" }

go build -o bin\simulator.exe .\cmd\simulator
if ($LASTEXITCODE -ne 0) { throw "simulator build failed" }

go build -o bin\web.exe .\cmd\web
if ($LASTEXITCODE -ne 0) { throw "web build failed" }

go build -o bin\internet.exe .\cmd\internet
if ($LASTEXITCODE -ne 0) { throw "internet build failed" }

Write-Host "[RUN] Starting server, simulator, web..."
Write-Host "[RUN] Starting internet poller (OpenSky)..."

# Start in separate windows so this shell stays usable.
Start-Process -WorkingDirectory $root -FilePath ".\bin\server.exe" -ArgumentList "-http :$ServerPort" -WindowStyle Minimized
Start-Sleep -Milliseconds 800
Start-Process -WorkingDirectory $root -FilePath ".\bin\simulator.exe" -ArgumentList "-target http://localhost:$ServerPort/ingest" -WindowStyle Minimized
Start-Sleep -Milliseconds 800
Start-Process -WorkingDirectory $root -FilePath ".\bin\web.exe" -ArgumentList "-http :$WebPort -root .\\web" -WindowStyle Minimized

Start-Sleep -Milliseconds 800
# Europe bbox (approx): minLat,minLon,maxLat,maxLon
# lat: 35..72, lon: -11..40
Start-Process -WorkingDirectory $root -FilePath ".\bin\internet.exe" -ArgumentList "-target http://localhost:$ServerPort/ingest -bbox 35,-11,72,40 -interval 20s" -WindowStyle Minimized

Start-Sleep -Seconds 1
Start-Process "http://localhost:$WebPort"

Write-Host "[RUN] OK: UI http://localhost:$WebPort (server http://localhost:$ServerPort)"