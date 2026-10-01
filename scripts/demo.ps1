$ErrorActionPreference = 'Stop'
Push-Location (Join-Path $PSScriptRoot '..')
try {
    go build -trimpath -o bin/watchdog.exe ./cmd/watchdog
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }

    docker compose -p watchdog-demo up -d
    if ($LASTEXITCODE -ne 0) { throw 'Demo containers could not start' }

    & .\bin\watchdog.exe --label=watchdog.demo=true --auto-restart --recover-exited
} finally {
    Pop-Location
}
