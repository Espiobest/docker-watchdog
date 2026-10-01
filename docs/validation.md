# Validation notes

Validated locally on October 1, 2026 with Go 1.26.8, Windows amd64, Docker Desktop, and Docker Engine 29.4.3 running Linux containers.

## Automated checks

- `go vet ./...` — passed.
- `go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...` — passed.
- `go test -race -cover ./...` — passed across all packages.
- `go build -trimpath -o bin/watchdog.exe ./cmd/watchdog` — passed.
- `docker build -t docker-watchdog:local .` — passed; CGO disabled.
- `docker compose -p watchdog-demo --profile service config --quiet` — passed.

Measured statement coverage: recovery core 91.8%, Docker adapter 87.7%, HTTP API 83.3%, storage 74.5%, terminal UI 77.2%, log output 78.1%, CLI 60.9%. These numbers measure executed statements, not proof of correctness.

## Real Docker checks

The isolated `watchdog-demo` Compose project was used for these checks:

1. Healthy and unhealthy containers produced real CPU, memory, and network observations.
2. The persistently unhealthy container was restarted twice with a configured two-attempt cap, then remained `retry-exhausted`.
3. Relaunching the watchdog with the same SQLite journal preserved the exhausted two-attempt budget and issued no additional recovery for that container.
4. A tracked container exiting with code 1 was recovered with `--recover-exited` enabled.
5. A newly created Docker-managed crash-loop container reached `crash-loop` after three observed restarts; Watchdog issued no recovery attempts for it.
6. The current-container and incident-history HTTP endpoints returned live observations and actions.
7. The interactive terminal dashboard rendered and refreshed in a Windows pseudoterminal, then restored the terminal on timed shutdown.
8. The built Linux service image monitored the Docker socket and exited cleanly after its configured runtime.

The dashboard image in the README comes from a deterministic UI test fixture, not a claimed production workload. Regenerate it by exporting `WATCHDOG_PREVIEW` for `TestPreview`, then running `scripts/render_preview.py` on that ANSI file.
