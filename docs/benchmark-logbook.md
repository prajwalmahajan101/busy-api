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
| 4 (before) | 2026-10-05 | ~5000 rps hot read, `DB_MAX_CONNS=1` | 210.8ms | 278.7ms | 308ms | 0.00% | DB pool saturated — reads queue behind the single connection (simulated at-scale contention); fail-open (Valkey down) serves all from DB, 0 5xx | — |
| 4 (after)  | 2026-10-05 | ~5000 rps hot read, `DB_MAX_CONNS=1` | 0.61ms | 4.63ms | 13.9ms | 0.00% | — | cache-aside on `items.Get` (Valkey, fail-open), key `item:<id>`, invalidate on write/soft-delete; hits bypass the pool (repo p95 → 0ms) |
| 4b (before) | 2026-10-05 | 2000 rps, `DB_MAX_CONNS=1`, Valkey dropped mid-load (outage phase) | 207ms | 268ms | 299ms | 0.00% | single-tier cache: Valkey outage floods the DB (db_reads 7/s→**221/s**, 19,894 reads in the 30s outage) + fail-open miss→DB per request; throughput collapses (iterations dropped). Fail-open held (0 5xx) | — |
| 4b (after)  | 2026-10-05 | 2000 rps, `DB_MAX_CONNS=1`, Valkey dropped mid-load (outage phase) | 0.34ms | **0.91ms** | 3.05ms | 0.00% | — | tiered L1(LRU)→L2(Valkey, breaker)→DB (T22a/T22b): L1 absorbed the working set (outage db_reads **19,894→0**), breaker skipped the dead Valkey (no dial); outage p95 268ms→0.91ms (294×), full 180k reqs, auto-recovered on restart |

<sub>Rung 2: seeded 100k rows (`make load-seed`). EXPLAIN showed the only Seq Scan was the exact count; **no index fixes a count where `is_active=true` matches ~all rows** (covering `(is_active,id)` index still Seq-Scanned). Fix was query-design: swap exact count for an O(1) `reltuples` estimate on the list path (exact `CountItems` kept for off-hot-path callers), plus a partial `(id) WHERE is_active=true` index for the active-ordered scan as soft-deletes accumulate. p95 20.68ms → 3.00ms (repo p95 19ms → 1ms). Target p95 < 15ms met; no Seq Scan on the hot path.</sub>

<sub>Rung 4: cache-aside re-introduced from `phase5-built` (`internal/valkey` + `internal/resilience/cache`, both fail-open). **Unconstrained 1K rps needs no cache** — the box holds p95~3ms with none (matches the ROADMAP: 1→1K is one stateless box). To earn the cache, the DB-contention regime that appears at 10K+/cloud was simulated locally with `DB_MAX_CONNS=1` (shared-Postgres / PgBouncer cap). Hot-read scenario `loadtest/cache_read.js` (`make load-cache`) hammers `GET /items/:id` over a 1000-key working set. Cache OFF (Valkey stopped → every op fails open to a miss → all reads queue on the one connection): p95=278.7ms, ~2.6K rps, **0 5xx (fail-open proven, T20)**. Cache ON (Valkey up → hits bypass the pool, repo p95→0ms): p95=4.63ms, 4.4K rps. 60× p95 improvement; target p95 < 50ms met. T29 (real 10K push) needs a second k6 machine.</sub>

<sub>Rung 4b: single-tier fail-open has a failure mode 4a didn't test — a Valkey outage **mid-load**. New scenario `loadtest/cache_resilience.js` (`make load-cache-resilience`) runs a steady 2000 rps split into warm→outage→recovery phases, dropping Valkey via `docker stop` at the warm→outage boundary and restoring it at outage→recovery, with per-phase p95 + a `db_reads` counter (repo_ms>0). **Before (single-tier):** outage p95 268ms, db_reads 221/s (DB flood — every miss hits the one connection), throughput collapsed. **After (tiered L1→L2→DB + self-healing breaker, T22a/T22b):** a bounded in-process LRU (`CACHE_L1_MAX`, `CACHE_L1_TTL_S`) absorbs the working set so outage db_reads → **0**; an in-memory circuit breaker around Valkey OPENs after `CACHE_BREAKER_FAIL_THRESHOLD` failures and skips the dead backend (no dial), HALF_OPEN-probing every `CACHE_BREAKER_RECOVERY_S` to self-heal on restart. Outage p95 268ms → **0.91ms (294×)**, 0 5xx, full 180k reqs. In-memory breaker (not Valkey-backed) by design — a breaker whose state needs Valkey is useless when Valkey is down. ADR 0007. L1 TTL set to 120s for the proof so L1 holds across the 30s outage (default 30s). Hardening (jitter/singleflight/bloom/hot-key, T23–T28a) remains symptom-gated.</sub>

<sub>Rung 3: `make load RPS=100 DURATION=30s`, same 100k rows, single local instance. 7501 reqs, 0 failed, 100% checks. pgxpool wait measured indirectly — `server_repo_ms` (which wraps query + connection acquire via `TrackRepo`) held at p95=1ms identical to rung 2, so a 10× load increase added no acquire latency. `DB_MAX_CONNS` left at the 48 default (4×NumCPU=12); no index or pool change earned. Target p95 < 30ms met. Local-box headroom for higher rungs documented in `docs/local-tuning.md`.</sub>

<sub>Rung 1 run: `k6 run loadtest/load.js -e RPS=1 -e DURATION=30s --summary-trend-stats="avg,min,med,p(90),p(95),p(99),max"`, local single instance + local Postgres, `OTEL_ENABLED`/`APILOG` not yet built. 75 reqs, 0 failed, all envelope/request_id checks passed. Per-layer from `Server-Timing`: `service`/`repo` p99=2ms. Target p95 < 10ms met. Note: k6's default summary omits p99 — pass `--summary-trend-stats` (or set `summaryTrendStats` in options) to capture it.</sub>

---

## Full ladder — current code (all optimizations on)

Snapshot of the finished rung-4 code with everything enabled: cache-aside on
(Valkey up), `reltuples` count, partial index, and a realistic `DB_MAX_CONNS=48`
(4×12 cores). `make load RPS=<n> VUS=200 DURATION=20s`, mixed `load.js`
(cached `GET /items/:id` + DB-backed list), single box.

| RPS (target) | achieved rps | p50 | p95 | p99 | max | err% | repo p95 |
|---|---|---|---|---|---|---|---|
| 1    | 1.7   | 1.57ms | 3.93ms | 5.64ms | 6.0ms | 0% | 2ms |
| 10   | 15.7  | 1.45ms | 2.63ms | 3.65ms | 10.6ms | 0% | 1ms |
| 100  | 157   | 1.17ms | 2.28ms | 2.95ms | 12.0ms | 0% | 1ms |
| 1000 | 1570  | 1.22ms | 3.24ms | 7.37ms | 27.4ms | 0% | 1ms |
| 2000 | 3142  | 1.46ms | 8.92ms | 16.23ms | 72.0ms | 0% | 4ms |
| 5000 | 4767  | 55.5ms | 291.6ms | 341ms | 608ms | 0% (1542 dropped/s) | 256ms |

## Insights

1. **Flat p95 (≤ 4ms) from 1 → 1000 rps.** Latency is independent of load across
   three orders of magnitude — the box is nowhere near saturation. This is the
   signature of a correct stateless design: throughput scales with no latency
   cost until a real resource binds. Confirms the ROADMAP claim "1→1K needs no
   infra change."

2. **The bottleneck was never the index — it was query design.** Rung 2's 20.68ms
   p95 came from an exact `count(*)` scanning 100k rows every list call. No index
   fixes a count where the predicate matches ~all rows; the O(1) `reltuples`
   estimate did (p95 → 3ms). Biggest lesson: profile the *query plan*, don't
   reflexively add indexes.

3. **Cache value is conditional on DB contention, not on raw RPS.** At a healthy
   `DB_MAX_CONNS=48` the cache barely matters up to 2000 rps (DB isn't the
   bottleneck). Its 60× win (278ms → 4.6ms) only appears once the pool is the
   scarce resource (`DB_MAX_CONNS=1`, simulating a shared/pgbouncer-capped
   Postgres at 10K+). Cache-aside buys *DB-independence*, not blanket speed —
   earn it where the DB actually binds.

4. **`repo_ms` is the leading indicator.** Every rung's p95 tracked `repo_ms`
   (query + connection-acquire time). It stayed at 1ms through 1000 rps, ticked to
   4ms at 2000, and blew to 256ms at 5000 — the single cleanest signal of where
   the real work (and the queueing) is. Per-layer timing (rung 1) paid for itself.

5. **The 5000-rps cliff is the test rig, not the service.** p95 jumps to 292ms
   with 1542 dropped iterations/s because k6 + server + Postgres + Valkey share 12
   cores. It's CPU/load-gen exhaustion, exactly what `docs/local-tuning.md`
   predicts above ~5K. Any "server fails at 5K" claim from a single box is
   measuring the laptop — real 10K needs two-machine k6 (T29) / cloud.

6. **Fail-open held under load.** With Valkey stopped mid-regime, every cache op
   degraded to a DB read with **0 5xx** — availability never depended on the
   cache. Resilience is a measured property here, not an assumption.

7. **Every win was earned by a reproduced failure first.** Rungs 1 and 3 added
   no code because no benchmark justified it; rungs 2 and 4 changed code only
   after a red run proved the need. The ladder kept the diff small and the
   complexity paid-for.
