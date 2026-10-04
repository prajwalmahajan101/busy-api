# busy-api — Benchmark Logbook

One row per rung run. Record a row **before climbing to the next rung** — the
RPS ladder (see `docs/ROADMAP.md`) earns complexity only where a bottleneck is
proven. Where a run breaks tells you the next rung's fix.

## Running the profiles

| Target | Profile |
|---|---|
| `make load-smoke` | 1 VU, 30s sanity; fails on any non-2xx or missing envelope field |
| `make load RPS=<n> DURATION=<d>` | ramp to target RPS; p95 budget |
| `make load-stress RPS=<n>` | ramp past target until thresholds break (writes `stress-summary.json`) |
| `make load-spike RPS=<n>` | 0 → peak → 0 burst |
| `make load-soak RPS=<n>` | 30m sustained; leak watch |
| `make load-matrix RPS=<n>` | OTEL × APILOG on/off delta (rung 5) |

Parameters: `BASE_URL` (default `http://localhost:8000`), `RPS`, `VUS`,
`DURATION`. Override on the command line, e.g. `make load RPS=1 DURATION=30s`.

**Grafana / Prometheus:** add `--out experimental-prometheus-rw` to the `k6 run`
line (set `K6_PROMETHEUS_RW_SERVER_URL`) so k6 metrics land next to server RED.

## Per-layer timing

Every response carries a standard header:

```
Server-Timing: handler;dur=<ms>, service;dur=<ms>, repo;dur=<ms>
```

k6 parses it (see `loadtest/lib/thresholds.js`) into the summary trends
`server_handler_ms`, `server_service_ms`, `server_repo_ms` — so latency is
attributable per layer from the client side, not just in server logs.

## Results

| Rung | Date | Load | p50 | p95 | p99 | err% | Bottleneck observed | Fix applied |
|------|------|------|-----|-----|-----|------|---------------------|-------------|
| 1    | 2026-10-05 | 1 rps | 1.61ms | 3.08ms | 7.39ms | 0.00% | baseline — none; repo≈service≈1ms (DB call is the floor, handler adds ~0) | — |

<sub>Rung 1 run: `k6 run loadtest/load.js -e RPS=1 -e DURATION=30s --summary-trend-stats="avg,min,med,p(90),p(95),p(99),max"`, local single instance + local Postgres, `OTEL_ENABLED`/`APILOG` not yet built. 75 reqs, 0 failed, all envelope/request_id checks passed. Per-layer from `Server-Timing`: `service`/`repo` p99=2ms. Target p95 < 10ms met. Note: k6's default summary omits p99 — pass `--summary-trend-stats` (or set `summaryTrendStats` in options) to capture it.</sub>
