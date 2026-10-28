# Intelligent Adaptive Load Balancer

A working Go HTTP reverse proxy with four interchangeable routing policies, active health checks, passive circuit breaking, bounded application retries, streaming, admission control, and a reproducible experiment harness. The proxy has no external Go dependencies.

This is a production-oriented portfolio implementation, not a claim of production certification or universal adaptive-routing superiority. See [validation](docs/VALIDATION.md) for what was actually run and [design](docs/DESIGN.md) for tradeoffs.

## Start the environment

Requirements: Docker with Compose. The native benchmark harness additionally needs Go 1.23+ (validated toolchain recorded in validation), k6 1.0.0, and Python 3.10+. CI and the Docker builder pin Go 1.27.3.

```sh
docker compose up --build -d
curl http://localhost:9090/ready
curl 'http://localhost:8080/example?hello=world'
docker compose --profile load run --rm k6
```

The proxy starts fail-closed and becomes ready after two successful checks. The first readiness probe can return 503 while the backend checks complete.

- Proxy: <http://localhost:8080>
- Metrics: <http://localhost:9090/metrics>
- Prometheus: <http://localhost:9091>
- Grafana: <http://localhost:3000>, user `admin`, password `local-experiments-only`

Grafana's dashboard and Prometheus data source are provisioned automatically. All published Compose ports bind to loopback. Override `GRAFANA_PASSWORD` and `SIM_TOKEN` for your environment. The simulator control API is for experiments and is served on a separate port, never through the proxy's data listener.

```sh
LB_ALGORITHM=least_connections docker compose up -d --force-recreate lb
# Valid values: round_robin, least_connections, weighted_round_robin, adaptive

docker compose down
```

## Build and run without Docker

```sh
make build
SIM_TOKEN=local-test bin/backend -id a -listen 127.0.0.1:8081 -admin 127.0.0.1:9081 -latency 10 -jitter 2 -capacity 8 -seed 1
# In separate terminals:
SIM_TOKEN=local-test bin/backend -id b -listen 127.0.0.1:8082 -admin 127.0.0.1:9082 -latency 30 -jitter 5 -capacity 32 -seed 2
SIM_TOKEN=local-test bin/backend -id c -listen 127.0.0.1:8083 -admin 127.0.0.1:9083 -latency 60 -jitter 10 -capacity 64 -seed 3
bin/lb -config configs/local.json
```

The benchmark runner starts and stops its own native processes; stop the above services (and Compose) before using it. Ports 8080–8083, 9081–9083, and 9090 must be free.

## Run the checks

```sh
make test
make race
make vet
make bench
go test -run '^$' -bench . -benchmem ./internal/pool
python3 -m unittest discover -s benchmarks -p 'test_*.py'
# Optional integration test against the disposable Compose stack (stops lb):
python3 scripts/validate_stack.py --project adaptive-lb
docker compose up -d lb
```

Tests cover deterministic distribution, removed backends, adaptive exploration and stale observations, concurrent reservation accounting, capacity limits, active-health thresholds and non-overlap, circuit generations and recovery, header sanitization, request replay safety, retry deadlines, partial responses, streaming cancellation, ambiguous HTTP framing, and simulator authorization.

## Run reproducible experiments

```sh
make build
python3 -m venv .venv
.venv/bin/pip install -r benchmarks/requirements.txt
# 4 algorithms × 5 workloads × 5 repetitions, 10 s warmup + 30 s measurement each.
python3 benchmarks/run.py --output benchmarks/results/full
.venv/bin/python benchmarks/analyze.py benchmarks/results/full
```

Use a new output directory for each matrix: the runner refuses to overwrite existing runs. Each run restarts all services with matched seeds and configuration. Algorithm and workload order are randomized within repetition blocks. The generator uses open-loop arrival rates and records dropped arrivals instead of hiding overload behind a closed-loop client.

For a short pipeline check:

```sh
python3 benchmarks/run.py --output benchmarks/results/quick \
  --repetitions 2 --duration 6 --warmup 2 --rate 300 --scenarios heterogeneous
.venv/bin/python benchmarks/analyze.py benchmarks/results/quick
```

Outputs include raw k6 summaries, service logs, before/after Prometheus snapshots, metadata, per-run CSV, mean/95% bootstrap interval CSV, paired adaptive-minus-baseline comparisons, a chart, and interpretation notes. The included `benchmarks/sample-results/smoke` and `dynamic-smoke` directories are actual short local runs, not production performance claims. See [benchmark methodology](docs/BENCHMARKS.md).

## Change backend behavior live

This replaces the simulator's entire behavior configuration; omitted numeric fields become zero. `capacity` must be provided. For example, slow backend A for ten seconds:

```sh
curl -X PUT http://localhost:9081/control \
  -H 'Authorization: Bearer local-experiments-only' \
  -H 'Content-Type: application/json' \
  -d '{"latency_ms":10,"jitter_ms":2,"capacity":8,"spike_ms":400,"spike_seconds":10}'
```

Other controls are `outage_seconds`, `error_probability` (0–1), and `cpu_iterations`. Outages affect active health checks; random application failures affect data requests and passive health. Capacity exhaustion produces 503. CPU work checks cancellation regularly. Randomness is seeded, but concurrent scheduling means request-by-request samples are not bit-for-bit reproducible.

## Configuration

`configs/local.json` lists all settings and defaults. Unknown fields, invalid destinations, duplicate IDs, invalid weights, nonpositive durations, unknown algorithms, and unsafe retry limits are rejected at startup. `LB_ALGORITHM` overrides only the algorithm.

Each backend has a stable ASCII `id` (letters, digits, dot, underscore, hyphen), a trusted `http`/`https` destination (optional base path), a positive capacity `weight`, and a hard `capacity` limit for in-flight requests. Capacity and weight are deliberately separate: capacity limits concurrency; weight expresses relative routing preference. Proxy capacity should match the experimental backend limit unless testing a deliberate mismatch.

Health and admission are shared by every algorithm. `max_attempts` is limited to 1–3. `retry_budget` bounds header acquisition for retry-eligible requests (including upload time); `header_timeout` also bounds each upstream response-header wait. Response streams are not capped by either timer after final headers arrive. A canceled downstream request cancels its upstream context.

Adaptive score parameters and their interpretation are detailed in [DESIGN.md](docs/DESIGN.md). Configuration is static: restart to change policies or the backend pool.

## Layout

```text
cmd/lb, cmd/backend       Executables and shutdown lifecycle
internal/config          Strict configuration and validation
internal/routing         Deterministic policy interface and scoring
internal/pool            Atomic reservations, observations, circuits, active health
internal/proxy           ReverseProxy, forwarding, transport, safe retry loop
internal/metrics         Bounded-label Prometheus exposition
internal/simulator       Reconfigurable experimental backends
benchmarks               k6 workloads, isolated runner, statistical analysis
configs, deploy          Local/Compose config, Prometheus, Grafana provisioning
.github/workflows        Formatting, vet, race tests, build, container CI
```

## Operating boundaries

Use a maintained Go release: HTTP parsing and transport security depend on the standard library. Backend URLs and DNS are trusted operator inputs; users cannot select destinations. The proxy rejects absolute-form requests, CONNECT, and HTTP upgrades. It reconstructs forwarding headers from the immediate connection and does not trust an upstream proxy chain. It preserves ordinary headers and lets `ReverseProxy` remove hop-by-hop fields.

The data listener supports HTTP streaming, not WebSocket/CONNECT tunnels. Deploy behind an appropriately configured TLS edge or private network. There is no built-in authentication, live service discovery, distributed coordination, or hot reload. Admin metrics have no authentication and must remain on a management interface. Long uploads/streams consume a bounded concurrency slot; add edge body-rate and stream-duration limits for untrusted Internet deployments.

The Go `Transport` can transparently repair a stale reused connection for replayable requests. `max_attempts` and retry metrics count this project's explicit backend selections, not those internal socket-level repairs. No application status retry happens after headers have been returned to the client. See the documented retry rules before enabling replayable writes in an embedded use case.
