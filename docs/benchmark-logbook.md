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
| 4b (48 conns) | 2026-10-05 | 2000 rps, `DB_MAX_CONNS=48`, Valkey dropped mid-load (outage phase) | 0.39ms | 1.02ms | 3.2ms | 0.00% | at a healthy pool the outage is already mild — 48 idle conns absorb the flood, so the 294× headline is a **low-pool** effect; the fix still keeps DB load flat + kills the cold-read dial tax (see note) | tiered L1+breaker (same build) |
| 4c-T23 (before) | 2026-10-05 | 500 rps hot read, `CACHE_ITEM_TTL_S=20`, L1 off, `DB_MAX_CONNS=1`; 1000 keys warmed in one burst | 0.98ms | 2.1ms | 2.7ms | 0.00% | **avalanche reproduced** — identical TTL + no-jitter `Set` means all 1000 keys expire in the same instant: `dbsize` collapses **1000→313 in ~1s** and DB reads spike **0/s → 356/s** (Valkey `keyspace_misses` delta) against a flat-zero baseline. Latency barely moves (sub-ms PK reads) — the harm is DB-read *volume*, not p95 | — (T24: ±jitter per Set) |
| 4c-T24 (after)  | 2026-10-05 | same regime, `CACHE_TTL_JITTER_PCT=10` (shipping default) | 1.0ms | 2.1ms | 2.7ms | 0.00% | — | ±10% L2 TTL jitter (`jitteredTTL` in `valkeyCache.Set`): the 1s cliff becomes a ~6s ramp — peak DB reads **356/s → 250/s**, `dbsize` min **313 → 646**. ±2s band on a 20s TTL, so partial by design; dose-response below |
| 4c-T25 (before) | 2026-10-05 | 3000 rps all on ONE hot key, `CACHE_ITEM_TTL_S=5`, jitter on (default), L1 off, `DB_MAX_CONNS=1` | 0.87ms | 3.28ms | 8.76ms | 0.00% | **stampede reproduced** — at each expiry of the single hot key, every concurrent in-flight request misses together and runs its own DB read before the first repopulates: **peak M = 41–63 duplicate DB reads for one logical value** (one expiry), 0 between, ~142–257 reads/run over ~8 cycles. Ideal = 1/expiry. Orthogonal to jitter (jitter desyncs *across* keys; one key still has one expiry instant) | — (T26: `singleflight`) |
| 4c-T26 (after)  | 2026-10-05 | same regime (both rows re-measured with the DB-layer instrument, Postgres `items.idx_scan`) | 0.94ms | 3.56ms | 7.29ms | 0.00% | — | `x/sync/singleflight` in `Service.Get`, keyed by cache key: concurrent same-key misses collapse to one DB read. Per-expiry DB reads **41 → 1** (peak), run total **142 → 8** (≈1/cycle = ideal). Latency unchanged (followers wait on the leader's sub-ms read instead of queuing on the 1 conn). Integration test `TestSingleflight_CollapsesConcurrentMisses` asserts it directly (1 `Set` for 50 barrier-synced concurrent misses) |

> **Caveat — every `4c-*` before-number is `DB_MAX_CONNS=1` *simulated* contention, not a healthy pool.** At the realistic `DB_MAX_CONNS=48` these herds/stampedes are absorbed by idle connections (sub-ms, no latency impact — insight #3/#8), so the 4c fixes are **insurance for the contended regime** (shared/pgbouncer-capped Postgres at 10K+ / cloud), not wins observed at a healthy pool on this box.

<sub>Rung 2: seeded 100k rows (`make load-seed`). EXPLAIN showed the only Seq Scan was the exact count; **no index fixes a count where `is_active=true` matches ~all rows** (covering `(is_active,id)` index still Seq-Scanned). Fix was query-design: swap exact count for an O(1) `reltuples` estimate on the list path (exact `CountItems` kept for off-hot-path callers), plus a partial `(id) WHERE is_active=true` index for the active-ordered scan as soft-deletes accumulate. p95 20.68ms → 3.00ms (repo p95 19ms → 1ms). Target p95 < 15ms met; no Seq Scan on the hot path.</sub>

<sub>Rung 4: cache-aside re-introduced from `phase5-built` (`internal/valkey` + `internal/resilience/cache`, both fail-open). **Unconstrained 1K rps needs no cache** — the box holds p95~3ms with none (matches the ROADMAP: 1→1K is one stateless box). To earn the cache, the DB-contention regime that appears at 10K+/cloud was simulated locally with `DB_MAX_CONNS=1` (shared-Postgres / PgBouncer cap). Hot-read scenario `loadtest/cache_read.js` (`make load-cache`) hammers `GET /items/:id` over a 1000-key working set. Cache OFF (Valkey stopped → every op fails open to a miss → all reads queue on the one connection): p95=278.7ms, ~2.6K rps, **0 5xx (fail-open proven, T20)**. Cache ON (Valkey up → hits bypass the pool, repo p95→0ms): p95=4.63ms, 4.4K rps. 60× p95 improvement; target p95 < 50ms met. T29 (real 10K push) needs a second k6 machine.</sub>

<sub>Rung 4b: single-tier fail-open has a failure mode 4a didn't test — a Valkey outage **mid-load**. New scenario `loadtest/cache_resilience.js` (`make load-cache-resilience`) runs a steady 2000 rps split into warm→outage→recovery phases, dropping Valkey via `docker stop` at the warm→outage boundary and restoring it at outage→recovery, with per-phase p95 + a `db_reads` counter (repo_ms>0). **Before (single-tier):** outage p95 268ms, db_reads 221/s (DB flood — every miss hits the one connection), throughput collapsed. **After (tiered L1→L2→DB + self-healing breaker, T22a/T22b):** a bounded in-process LRU (`CACHE_L1_MAX`, `CACHE_L1_TTL_S`) absorbs the working set so outage db_reads → **0**; an in-memory circuit breaker around Valkey OPENs after `CACHE_BREAKER_FAIL_THRESHOLD` failures and skips the dead backend (no dial), HALF_OPEN-probing every `CACHE_BREAKER_RECOVERY_S` to self-heal on restart. Outage p95 268ms → **0.91ms (294×)**, 0 5xx, full 180k reqs. In-memory breaker (not Valkey-backed) by design — a breaker whose state needs Valkey is useless when Valkey is down. ADR 0007. L1 TTL set to 120s for the proof so L1 holds across the 30s outage (default 30s). Hardening (jitter/singleflight/bloom/hot-key, T23–T28a) remains symptom-gated.</sub>

<sub>Rung 4b — pool-size sweep + the unmasked dial tax. Re-ran the same scenario at the realistic `DB_MAX_CONNS=48`: outage p95 only **1.02ms** (tiered) and ~0.9ms even single-tier — 48 idle connections absorb the DB flood, so a Valkey outage is already mild on latency at a healthy pool. **The 294× headline is a low-pool effect** (it needs the DB pool to be the scarce resource, i.e. a shared/pgbouncer-capped Postgres at 10K+) — consistent with insight #3. What the fix still buys at 48 conns: DB load stays flat during the outage, and it removes the **per-cold-read dial tax**. A single-request micro-test (Valkey down, one cold `GET /items/:id`) isolates it: **L1 off → 3577ms** (go-redis hangs ~3.5s dialing the dead backend before the breaker opens); **L1 on → 0ms** (served from L1). The loaded k6 *masks* this — under 2000 rps the breaker trips in the first ~3.5s window, then the DB path (queued at 1 conn / sub-ms at 48) dominates — which is why the dial tax shows up starkly only in the micro-test. **Measurement caveat:** the `db_reads` counter keys on `Server-Timing repo;dur>0`, and that header is **integer-ms**, so sub-ms DB reads (what 48 idle conns deliver) round to 0 and are undercounted — `db_reads` is reliable only under real contention (`DB_MAX_CONNS=1`, reads ≫1ms, e.g. the 19,894 above); at 48 conns trust latency, not the counter. **Follow-up (not in this change):** cap go-redis `DialTimeout` (~200ms) in `internal/valkey/client.go` so the breaker trips faster and the outage-onset spike is bounded even before it OPENs.</sub>

<sub>Rung 4c — T23 (avalanche proof, no fix yet). New scenario `loadtest/cache_avalanche.js` (`make load-cache-avalanche`) warms every key in one burst (`shared-iterations`, so they share an absolute expiry), then reads steadily across the TTL boundary. **DB-read volume is measured from Valkey `keyspace_misses`, not the k6 `db_reads` counter** — the 4b caveat above proved `Server-Timing repo;dur` rounds sub-ms PK reads to 0, so that detector is blind to a fast herd; with L1 off every L2 miss is exactly one DB read, so the per-second `keyspace_misses` delta (polled by the make target) is the exact DB-read rate. Run with `CACHE_ITEM_TTL_S=20 CACHE_L1_ENABLED=false` (short TTL lands the avalanche in-window; L1 off so its own TTL can't mask the L2 expiry). **Result:** flat 0/s baseline for ~15s, then at the synchronized boundary `dbsize` craters 1000→313 and DB reads spike to **356/s** (a weaker echo follows as the re-cached — still identical-TTL — keys expire together again). This is the thundering herd the ladder predicted; T24 adds ±jitter to every `Set` (L1 + L2) and must flatten the spike into a smear. Latency does not move on this box (a PK read is sub-ms even under the burst) — the avalanche is a DB-load problem, not a tail-latency one, which is itself the lesson: pick the instrument (miss count) that matches the symptom.</sub>

<sub>Rung 4c — T24 (avalanche cure: ±jitter). `jitteredTTL(base, pct)` (`internal/resilience/cache/jitter.go`, `math/rand/v2`) spreads the L2 `Set` TTL by ±`CACHE_TTL_JITTER_PCT` (default 10) at the single L2 write point (`valkeyCache.Set`), so burst-warmed keys no longer share one expiry instant. Re-ran the T23 scenario (same regime). **Dose-response** (peak DB reads/s · `dbsize` min): no jitter **356/s · 313** (1s cliff) → ±10% **250/s · 646** (~6s ramp) → ±50% **81/s · 926** (flat ~50/s hum, spike gone). The band is ±pct·TTL, so at the proof's short 20s TTL ±10% is only ±2s — partial smearing; at the production 300s TTL the same ±10% is a ±30s band (60s spread) and flattens completely. **Scope: L2 only** (confirmed). L1 (`expirable.LRU`) has a fixed construction-time TTL and ignores per-call ttl, so per-entry jitter would mean swapping the lib; and an L1-expiry wave falls through to the (now-jittered) L2 as an L2 *hit*, not a DB read — no DB herd — so the reproduced symptom is fully addressed at L2. Unit test `jitter_test.go` asserts the ±band + variation + no-op guards (pct=0, base=0→no expiry). Next symptom, T25: `singleflight` stampede (one hot key expiring mid-load → M duplicate DB reads).</sub>

<sub>Rung 4c — T25 (stampede proof, no fix yet). New scenario `loadtest/cache_stampede.js` (`make load-cache-stampede`) points all load at a single hot key (`HOT_ID`) at 3000 rps with a short 5s TTL, so the run sees ~6–8 expiry cycles; the make target polls Valkey `keyspace_misses`/s. **This is a different failure from the avalanche and it survives the T24 jitter fix on purpose** — jitter desyncs expiry *across* keys, but one key still has a single expiry instant, and at that instant every concurrent in-flight request misses and runs its own DB read before the first `Set` repopulates the cache. Result: each cycle is a burst of **M = 58–63 duplicate DB reads for one logical value** (0 between, all hits), where an ideal cache does exactly 1 (the rest wait on it). Run L1 off (else L1 serves the hot key and hides the L2 stampede) and `DB_MAX_CONNS=1` (widens the repopulation window so the pileup is observable — on a healthy pool the window is sub-ms and M shrinks, the dual of insight #3/#8: the stampede is contention-gated too). p95 stays 3.3ms, 0 5xx — like the avalanche this is a wasted-DB-work problem, not a latency one. T26 collapses concurrent same-key misses with `golang.org/x/sync/singleflight` → exactly 1 DB read per expiry; re-run must flatten each burst to 1.</sub>

<sub>Rung 4c — T26 (stampede cure: `singleflight`). `Service.Get` wraps the miss→`GetItem`→`Set` block in `s.sf.Do(key, …)` (`golang.org/x/sync/singleflight`, promoted from indirect): when a hot key expires, the first goroutine does the DB read + repopulate and all concurrent followers wait and share its result. Per-expiry DB reads **41 → 1** (peak), total **142 → 8** over the run (≈1 per expiry = ideal), 0 5xx, p95 flat at ~3.5ms. **Instrument correction — second occurrence of the T23 lesson.** The T25 proof polled Valkey `keyspace_misses`, which counts *cache* misses; singleflight collapses the *DB* reads **downstream** of the misses while the miss count is unchanged (every request still runs its own `cache.Get`). Measured at the cache, the fix looks like it does nothing (misses still ~350/run); measured at the DB (`pg_stat_user_tables.idx_scan` on `items`, one PK scan per `GetItem`) it's a 41→1 collapse. `loadtest/cache_stampede.js` + `make load-cache-stampede` now poll `idx_scan`, not `keyspace_misses` — measure the layer the symptom lives in. Deterministic regression: `TestSingleflight_CollapsesConcurrentMisses` (integration) uses a barrier cache that releases 50 callers simultaneously and asserts exactly one `Set` (= one DB load). Two ponytail caveats in the code: `Do` shares the leader's ctx (a cancelled leader fails its followers — fine for a sub-ms PK read; `DoChan` if the loader grows slow), and no in-closure cache re-check (singleflight already collapses the burst). **Ops gotcha:** the integration suite's `setupPool` `TRUNCATE`s `items`, destroying the 100k k6 seed — re-run `make load-seed N=100000` after `go test -tags=integration` before any cache k6 proof. Next symptom, T27: XFetch (probabilistic early recompute) — only if p99 still spikes at the TTL boundary after singleflight.</sub>

<sub>Rung 4c — T27 (XFetch) **go/no-go → NOT EARNED (no real boundary spike; the "spike" was a measurement bug).** The task is gated on "does p99 still spike at the TTL boundary after singleflight?" First pass with `loadtest/cache_boundary.js` (one hot key, jitter off, TTL=10s, tag `boundary`=±0.7s of an expiry vs `steady`) seemed to find a pool-independent ~3.6× spike (boundary p99 **40.7ms** vs steady 11.2ms @48 conns). **It was an artifact.** A control run with the key set to never expire in-window (`CACHE_ITEM_TTL_S=120` → pure hits, zero DB reads, zero refreshes) STILL showed boundary p99 **95ms** — impossible if the boundary were a cache event. Root cause: `phase()` tagged `elapsed % TTL ≈ 0`, which includes **t=0** — k6's ramp-up of 400 VUs + cold connections at 3000 rps. That startup latency poisoned the `boundary` bucket. Fixed `phase()` to skip the first full cycle (`elapsed < TTL` → `warmup`); re-measured with **SWR off, singleflight on**: boundary p99 **11.7ms** ≈ steady **10.1ms**, p99.9 21.3 both, max 27 vs 46 — **flat. At 3k rps (1 and 48 conns), singleflight (T26) already handles the TTL boundary completely; there is no residual spike.** **Status: DEFERRED, not closed.** The single box caps at ~5k (insight #5), so the 10k+ target load is **untested** — "no spike at ≤3k" is not "no spike ever," so T27 is parked pending higher-load evidence (T29 two-machine / 10k+), not skipped. A stale-while-revalidate prototype (envelope + async refresh + `CACHE_SWR_GRACE_PCT`) was built, measured against the corrected scenario (made no difference — nothing to fix *at these loads*), and **reverted** (~100 lines): building on absent evidence is premature. The corrected `cache_boundary.js` is kept as the evidence. Lesson: a signal needs a null-hypothesis control before you build against it (`2026-10-05-benchmark-instrument-matches-symptom`, extended). Revisit T27 at T29, or if a change makes the recompute expensive (slow query / aggregation) or hot-key TTLs much shorter.</sub>

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

8. **Resilience wins are contention-gated too — and the regime you test in
   decides what you see.** The rung-4b tiered cache shows 294× at
   `DB_MAX_CONNS=1` but ~1ms either way at `DB_MAX_CONNS=48`: a Valkey outage is
   a crisis only when the DB pool is already the scarce resource. Same code, two
   verdicts. Two corollaries bit here: (a) the per-request **dial tax** to a dead
   Valkey (3577ms/cold read) is hidden by concurrent load and by a slow DB queue —
   you only see it in a single-request micro-test; (b) an integer-ms timing header
   silently **undercounts sub-ms DB reads**, so a flood metric that works at 1 conn
   lies at 48. Pick the failure regime deliberately, and confirm your instrument
   has the resolution for it.

9. **Measure the layer the symptom lives in — this bit three times.** The cache
   failures (T23–T26) are DB-*work* problems, invisible in p95 (sub-ms PK reads
   never move the tail). Each needed a count metric at the right layer, and the
   obvious instrument was wrong twice: (a) `Server-Timing repo_ms` is integer-ms →
   rounds sub-ms reads to 0 (blind to the avalanche herd); (b) Valkey
   `keyspace_misses` counts *cache* misses, which `singleflight` leaves unchanged
   while it collapses the *DB* reads downstream — so the stampede fix looks like a
   no-op at the cache (misses ~350/run either way) and a 41→1 collapse at the DB
   (`pg_stat_user_tables.idx_scan`). Rule: name the exact quantity the failure
   moves — DB read *count*, not request latency, not cache misses — and read it off
   the layer that quantity lives on. A fix that's invisible in your dashboard is
   usually a dashboard pointed at the wrong layer, not a fix that did nothing.

10. **Jitter and singleflight are orthogonal stampede defenses — don't conflate
    them.** Both attack cache-expiry thundering herds, on different axes: TTL jitter
    (T24) desyncs expiry *across many keys* (fixes the avalanche); `singleflight`
    (T26) collapses concurrent misses *on one key* (fixes the dogpile). A single
    hot key defeats jitter entirely — it still has one expiry instant — which is
    why T25 reproduced a 41–63× pileup *with jitter already on*. One technique is
    not a substitute for the other; each was earned by its own reproduced failure,
    and a cache serving both a broad working set and a few hot keys needs both.
