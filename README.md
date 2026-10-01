# Docker Watchdog

**A terminal dashboard and recovery service for Docker containers, written in Go.**

Monitor container health and resource usage concurrently, detect crash loops, and recover unhealthy containers with bounded retries. Recovery state survives watchdog restarts; incident history is available through a local HTTP API.

![Watchdog terminal dashboard](docs/dashboard.svg)

*Dashboard preview rendered from the UI's deterministic test fixture; values are illustrative.*

## Features

- **Interactive dashboard:** colored statuses, CPU/memory/network totals, selected-container details, recent transitions, scrolling, and sorting.
- **Concurrent monitoring:** one goroutine per tracked container, a shared API concurrency limit, and a bounded fan-in channel.
- **Controlled recovery:** exponential backoff, retry caps, pre-restart state checks, and a continuous-health reset period.
- **Crash-loop detection:** tracks changes in Docker's restart counter and process start timestamps within a rolling window.
- **Durable state:** SQLite transactions commit recovery reservations before Docker receives a restart request.
- **Incident history:** deduplicated transitions and recovery actions, retained across process restarts.
- **Read-only HTTP API:** current observations, cursor-paginated events, and Prometheus-compatible metrics.
- **Service-friendly output:** plain transition logs or JSON Lines; graceful shutdown on Ctrl+C/SIGTERM.

No email integration or external database server is required.

## Requirements

- Go **1.26+** for building. `go.mod` selects the tested toolchain automatically when toolchain downloads are enabled.
- A running Docker Engine or Docker Desktop with **Linux containers**. Validated locally against Docker Engine 29.4.3.
- Docker Compose for the demo.
- A terminal with ANSI support for the dashboard. Windows Terminal, Linux terminals, and macOS terminals work; redirected output automatically uses logs.

The SDK negotiates the Docker API version. The binary can run on Windows while monitoring Docker Desktop's Linux engine. CPU and memory calculations target Linux container statistics.

## Quick start

From the repository directory:

```sh
go build -trimpath -o bin/watchdog ./cmd/watchdog
./bin/watchdog
```

On Windows / PowerShell:

```powershell
go build -trimpath -o bin/watchdog.exe ./cmd/watchdog
.\bin\watchdog.exe
```

**The default mode only observes.** It creates `watchdog.db` in the current directory, opens the dashboard when attached to a terminal, and serves the read-only API at `http://127.0.0.1:9780`.

Enable recovery for a chosen label:

```sh
./bin/watchdog --label=watchdog.enable=true --auto-restart
```

Label a container when creating it:

```sh
docker run -d --name example --label watchdog.enable=true nginx:alpine
```

Health-check recovery requires a Docker `HEALTHCHECK`. A running container without one is shown as `running`, not `healthy`.

### Dashboard keys

| Key | Action |
| --- | --- |
| `↑` / `↓` or `k` / `j` | Select a container |
| `PgUp` / `PgDn` | Scroll by a page |
| `Home` / `g`, `End` / `G` | First / last container |
| `s` | Cycle sorting: name, CPU, memory |
| `q` / `Ctrl+C` | Exit gracefully |

The dashboard updates automatically when containers stop, start, or disappear in Docker Desktop or another terminal. Polling defaults to 3 seconds and discovery to 5 seconds. Slow API calls can increase the time before an observation arrives; each row shows its sample age.

The dashboard currently observes and reports recovery actions. Interactive stop/start/restart buttons are not implemented yet.

## Try the failure demo

The Compose file provides four labeled containers:

| Container | Behavior | Expected result |
| --- | --- | --- |
| `healthy` | Always passes its health check | Remains healthy; no intervention |
| `unhealthy` | Always fails its health check | Backoff → restart → retry limit |
| `crashloop` | Exits repeatedly under Docker's `on-failure` policy | Crash-loop alert; Docker retains recovery ownership |
| `recoverable` | Exits with code 1 after 20 seconds | Watchdog recovers it with `--recover-exited` |

Linux / macOS:

```sh
make demo
```

PowerShell:

```powershell
.\scripts\demo.ps1
```

Or run the commands directly:

```sh
docker compose -p watchdog-demo up -d
go run ./cmd/watchdog --label=watchdog.demo=true --auto-restart --recover-exited
```

Start the watchdog immediately after the demo containers. It deliberately ignores previously stopped containers with no saved monitoring state. If `recoverable` exits before discovery, start it again while the watchdog is running:

```sh
docker compose -p watchdog-demo restart recoverable
```

Quit the watchdog, then clean up only the demo stack:

```sh
docker compose -p watchdog-demo --profile service down
```

This leaves the named journal volume intact. To rerun an exhausted recovery scenario, recreate its container (new container ID), or allow the existing container to become continuously healthy long enough to reset its budget.

## How recovery works

```mermaid
flowchart LR
    Docker[Docker Engine] --> Discovery[Periodic discovery]
    Discovery --> A[Container worker A]
    Discovery --> B[Container worker B]
    A --> Policy[Per-container recovery policy]
    B --> Policy
    Policy --> DB[(SQLite journal)]
    DB -->|reservation committed| Restart[Recheck and restart]
    Restart --> Docker
    A --> FanIn[Bounded event channel]
    B --> FanIn
    FanIn --> Output[Dashboard or logs]
    DB --> API[Read-only HTTP API]
```

1. Discovery lists containers matching the optional label. It adopts running/restarting containers and retains their workers when they later stop. With exit recovery enabled, previously tracked exited containers can also be restored from the journal.
2. A worker inspects its container and fetches a bounded, non-streaming stats response. The shared semaphore limits simultaneous Docker operations.
3. The policy classifies Docker lifecycle and health separately from watchdog assessments such as `backoff` and `retry-exhausted`. It observes new starts to detect loops and interrupt the stable-health timer.
4. An eligible failure first waits 5 seconds. Subsequent cooldowns are 10, 20, 40 seconds, up to the configured maximum. Only three attempts are allowed by default.
5. Before each restart, the worker commits its retry reservation to SQLite. It then rechecks the container to avoid acting on a stale observation and calls Docker's restart API with a 10-second stop grace period.
6. Failed and timed-out recovery requests consume attempts too. A timeout does not prove Docker did nothing. Cooldown begins after the request completes.
7. The retry budget resets only after 5 minutes of continuously observed healthy/running state without new starts. Watchdog downtime does not count toward that period.

### Recovery rules and limits

- **Observe-only by default.** `--auto-restart` enables mutations.
- **Exit recovery is separate.** `--recover-exited` permits recovery of previously observed containers that exit nonzero. Clean exits remain stopped. An external manual stop can produce a nonzero exit; polling cannot reliably distinguish that from a crash.
- **Paused, removing, dead, and health-starting containers are not restarted.** A crash-loop alert alone does not kill a currently healthy process.
- **Docker restart policies take precedence.** Watchdog does not restart containers configured with `always`, `unless-stopped`, or `on-failure`, including unhealthy ones. Docker itself generally responds to exits, not health-check failures; use restart policy `no` if Watchdog should own recovery.
- **Read errors do not imply unhealthy.** Failed inspections produce `unknown`; stats failures do not erase known health. Discovery failures retain existing workers and budgets.
- **Storage failures block recovery.** An uncommitted reservation never leads to a restart request. An OS lock prevents two watchdogs from opening the same journal, including across SQL connection replacement.
- **One controller per container set.** Different database files do not coordinate ownership. Avoid running multiple watchdog processes against overlapping labels.
- **Polling has limits.** Very short-lived containers can appear and disappear between discoveries. Historical restarts before the first observation are not automatically classified as a new crash loop.
- **No exactly-once claim.** If the process dies during an API request, the reserved attempt remains spent even if the request never reached Docker. This favors preventing restart storms.

## HTTP API

The API is read-only and binds to loopback by default. It has request timeouts and no authentication; use an authenticated reverse proxy if you expose it beyond the local machine.

| Endpoint | Returns |
| --- | --- |
| `GET /healthz` | Process/storage liveness, **not** a guarantee of Docker connectivity |
| `GET /api/containers` | Latest observations from this watchdog run |
| `GET /api/events?limit=100` | Newest incident records first |
| `GET /api/events?container=FULL_ID&before=SEQUENCE&limit=50` | Filtered history with cursor pagination |
| `GET /metrics` | Container count, recovery attempts, CPU, memory, and observation timestamps |

```sh
curl http://127.0.0.1:9780/api/containers
curl 'http://127.0.0.1:9780/api/events?limit=10'
curl http://127.0.0.1:9780/metrics
```

PowerShell: use `Invoke-RestMethod http://127.0.0.1:9780/api/containers`.

History retains the latest **10,000 transitions/actions**, rather than every stats sample. `limit` must be 1–500; `before` is an exclusive event-sequence cursor. Current rows include timestamps so clients can detect stale data. Recovery-attempt metrics are gauges because stable recovery resets them.

## Service mode

Plain logs or JSON Lines work with systemd, containers, and log collectors:

```sh
./bin/watchdog --output=json --label=watchdog.enable=true --auto-restart
```

Run the included demonstration service in Docker:

```sh
docker compose -p watchdog-demo --profile service up --build -d
docker compose -p watchdog-demo logs -f watchdog
```

The service stores its journal in a named volume and exposes the API on host loopback. Do not run the local demo watchdog at the same time. The Docker socket grants control of the host's containers; the service requires that access to perform recovery.

## Configuration

Run `watchdog --help` for every flag. Frequently used options:

| Flag | Default | Purpose |
| --- | --- | --- |
| `--output` | `auto` | Dashboard in a terminal, otherwise logs; also accepts `table`, `logs`, `json` |
| `--label` | all | Limit discovery to a Docker label |
| `--auto-restart` | false | Enable health recovery |
| `--recover-exited` | false | Include previously observed nonzero exits |
| `--interval` | `3s` | Poll interval per container |
| `--discovery-interval` | `5s` | Find new/removed containers |
| `--concurrency` | `8` | Maximum concurrent Docker operations |
| `--backoff` / `--max-backoff` | `5s` / `1m` | Initial and maximum recovery delays |
| `--max-retries` | `3` | Attempts before recovery is suspended |
| `--stable-reset` | `5m` | Continuous good health before retry reset |
| `--crash-threshold` / `--crash-window` | `3` / `1m` | Repeated-start detection |
| `--db` | `watchdog.db` | Durable journal file |
| `--listen` | `127.0.0.1:9780` | HTTP address; `--listen=` disables it |
| `--host` | SDK default | Docker endpoint override |
| `--run-for` | `0` | Optional duration before graceful exit |

`DOCKER_HOST`, `DOCKER_TLS_VERIFY`, and `DOCKER_CERT_PATH` are supported through the SDK. Docker CLI contexts are not automatically read. If the CLI works but Watchdog cannot connect, pass the context's endpoint explicitly:

```sh
./bin/watchdog --host="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
```

PowerShell:

```powershell
$dockerEndpoint = docker context inspect --format '{{.Endpoints.docker.Host}}'
.\bin\watchdog.exe --host=$dockerEndpoint
```

The SDK supports its native Unix socket, Windows named pipe, and TCP/TLS transports; SSH contexts require a separately configured tunnel.

## Development

```sh
go fmt ./...
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
go test -race -cover ./...
go build ./cmd/watchdog
```

The race detector requires a C toolchain even though the application and SQLite driver build without CGO. CI runs formatting, vet, Staticcheck, tests with race detection, and a build on Linux.

```text
cmd/watchdog/          CLI options and process lifecycle
internal/watchdog/     Worker supervision, typed statuses, policy, checkpoints
internal/dockerengine/ Docker SDK transport and Linux stats conversion
internal/storage/      SQLite schema, atomic journal, process ownership lock
internal/httpapi/      Read-only HTTP routes and metrics
internal/tui/          Bubble Tea state/update loop and Lip Gloss rendering
internal/display/      Plain and JSON log output
scripts/               Demo and preview helpers
```

Tests cover backoff/caps, interrupted health resets, durable reservations, storage failures, cancellation under backpressure, worker lifecycle/concurrency, HTTP SDK behavior, database reopen/connection replacement, API validation, and terminal layout/keyboard behavior. Unit tests do not require Docker.

## Possible next steps

- [ ] Confirmed stop/start/restart controls with persistent maintenance mode.
- [ ] Container log tailing from the selected row.
- [ ] Resource-history sparklines and threshold alerts.
- [ ] Docker event-stream reconciliation for short-lived containers.
- [ ] Multi-host monitoring with explicit ownership coordination.
