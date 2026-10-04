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
| 2 (before) | 2026-10-05 | 10 rps | 10.71ms | 20.68ms | 23.99ms | 0.00% | exact `count(*) WHERE is_active=true` Seq-Scans whole table (~23ms) on every list; GetItem/ListItems already index-backed | — |
| 2 (after)  | 2026-10-05 | 10 rps | 1.94ms  | 3.00ms  | 3.52ms  | 0.00% | — | approximate total via `reltuples` (`store.CountItemsEstimate`); partial index `idx_items_active_id (id) WHERE is_active=true` replaces low-value bool index |
| 3 | 2026-10-05 | 100 rps | 1.70ms | 2.73ms | 3.36ms | 0.00% | none — pgxpool uncontended: repo p95 flat at 1ms vs rung 2 (acquire wait ≈ 0) | no tuning needed; `DB_MAX_CONNS`=48 (4×12 cores) ample (~0.2 conns needed by Little's law) |

<sub>Rung 2: seeded 100k rows (`make load-seed`). EXPLAIN showed the only Seq Scan was the exact count; **no index fixes a count where `is_active=true` matches ~all rows** (covering `(is_active,id)` index still Seq-Scanned). Fix was query-design: swap exact count for an O(1) `reltuples` estimate on the list path (exact `CountItems` kept for off-hot-path callers), plus a partial `(id) WHERE is_active=true` index for the active-ordered scan as soft-deletes accumulate. p95 20.68ms → 3.00ms (repo p95 19ms → 1ms). Target p95 < 15ms met; no Seq Scan on the hot path.</sub>

<sub>Rung 3: `make load RPS=100 DURATION=30s`, same 100k rows, single local instance. 7501 reqs, 0 failed, 100% checks. pgxpool wait measured indirectly — `server_repo_ms` (which wraps query + connection acquire via `TrackRepo`) held at p95=1ms identical to rung 2, so a 10× load increase added no acquire latency. `DB_MAX_CONNS` left at the 48 default (4×NumCPU=12); no index or pool change earned. Target p95 < 30ms met. Local-box headroom for higher rungs documented in `docs/local-tuning.md`.</sub>

<sub>Rung 1 run: `k6 run loadtest/load.js -e RPS=1 -e DURATION=30s --summary-trend-stats="avg,min,med,p(90),p(95),p(99),max"`, local single instance + local Postgres, `OTEL_ENABLED`/`APILOG` not yet built. 75 reqs, 0 failed, all envelope/request_id checks passed. Per-layer from `Server-Timing`: `service`/`repo` p99=2ms. Target p95 < 10ms met. Note: k6's default summary omits p99 — pass `--summary-trend-stats` (or set `summaryTrendStats` in options) to capture it.</sub>
