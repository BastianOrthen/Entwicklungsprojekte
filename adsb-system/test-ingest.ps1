# Simple test script: POST simulated aircraft to server /ingest
# Run: powershell -ExecutionPolicy Bypass -File test-ingest.ps1

$url = "http://localhost:8080/ingest"

Write-Host "Posting simulated aircraft to $url"
Write-Host "Press Ctrl+C to stop"

$lat = 50
$lon = 8

for ($i = 0; $i -lt 100; $i++) {
    $lat += (Get-Random -Minimum -2 -Maximum 2)
    $lon += (Get-Random -Minimum -2 -Maximum 2)
    $alt = 5000 + (Get-Random -Minimum 1000 -Maximum 25000)
    $spd = 200 + (Get-Random -Minimum 0 -Maximum 300)
    $icao = [System.BitConverter]::ToString([System.Text.Encoding]::ASCII.GetBytes([string]((Get-Random -Minimum 100000 -Maximum 999999)))).Replace('-','').Substring(0,6)

    $data = @{
        icao = $icao
        lat = $lat
        lon = $lon
        alt = $alt
        speed = $spd
        seen = [System.DateTime]::UtcNow.ToString("o")
    } | ConvertTo-Json

    try {
        $resp = Invoke-WebRequest -Uri $url -Method Post -ContentType "application/json" -Body $data -UseBasicParsing
        Write-Host "[$(Get-Date -Format 'HH:mm:ss')] POST $icao → $($resp.StatusCode)"
    } catch {
        Write-Error "Failed: $_"
    }

    Start-Sleep -Milliseconds 1000
}
