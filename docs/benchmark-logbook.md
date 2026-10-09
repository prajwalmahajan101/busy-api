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
| 4c-T28 (before) | 2026-10-05 | 3000 rps target, every request a **fresh random absent id** (`100M+rand`, seed is ids 2–100001), bloom off, L1 off, `DB_MAX_CONNS=1` | 263.6ms | 369ms | 409.7ms | 100%† | **penetration reproduced** — no id ever resolves, so nothing caches a hit and no two requests share a key (singleflight/L1/L2 all inert). Every request falls through to `GetItem` → `ErrNoRows` → one PK index scan: **total DB reads = 43,867 ≈ served requests (44,205); peak db_reads/s = 3,054 tracks RPS 1:1**. The 1-conn pool saturates → p95 **369ms** (1.8× the 200ms budget), actual throughput throttled to ~1.46k rps (45.8k iters dropped). †100% `http_req_failed` = all 404 by design (not errors). At `DB_MAX_CONNS=48` latency is fine (p95 27ms) but the DB still eats 100% of the flood (**87,685 scans / 30s, peak 4,398/s**) — the problem is sustained wasted DB work, not tail latency | — (bloom pre-filter + negative cache) |
| 4c-T28 (after)  | 2026-10-05 | same regime, `CACHE_BLOOM_ENABLED=true` (default), `DB_MAX_CONNS=1` | 0.47ms | 3.48ms | 14.3ms | 100%† | — | In-process bloom pre-filter (`bits-and-blooms/bloom/v3`) + negative-cache tombstone on `ErrNoRows`. **DB reads 43,867 → 0** (total), **3,054/s → 0/s** (peak). Every absent id is answered in-process before any cache or DB touch. p95 **369ms → 3.5ms** (latency is a side effect of not touching the pool at all). Full 3k rps served (0 dropped vs 45.8k dropped). Negative-cache tombstone covers the bloom's false positives (gap ids that pass the filter reach the DB once, then are cached as absent for `CACHE_NEG_TTL_S=30`). Tests: `TestPresence_NoFalseNegatives`, `TestBloomShortCircuit_Integration`, `TestNegativeCache_Integration` |
| 4c-T28a | 2026-10-05 | 3000 rps single hot key (`HOT_ID=2`), **L1 ON** (shipping config), `CACHE_ITEM_TTL_S=300`, `DB_MAX_CONNS=1` | 0.60ms | 1.54ms | 6.04ms | 0.00% | **not earned** — L1 absorbs the hot key entirely in-process: **db_reads = 2** (initial fill + 1 L1-TTL re-read), **valkey_hits = 5** (initial fill only), 89,993/90,000 requests served from L1 (map lookup, no network). The hot-key Valkey-saturation failure mode cannot reproduce with L1 on — there is no Valkey connection to saturate. Key-splitting is for a multi-shard L2-only architecture where no L1 exists | — (not needed) |
| 5 (list cache) | 2026-10-07 | 500 rps, 500 list pages, `CACHE_ITEM_TTL_S=20`, L1 off, `DB_MAX_CONNS=48`, 600k rows | 1.22ms | 53.73ms | 1.96s | 0.69% | cache-aside + singleflight on `Service.List`, versioned key `items:list:<ver>:<page>:<size>` (version-key invalidation on write — see note), same TTL/jitter as single-item reads. Exact `count(*)` replaces `reltuples` estimate (accuracy over O(1) — the cache absorbs the cost). `CountItemsEstimate` + `RowQuerier` deleted | cache-aside + singleflight + version invalidation |
| 5 (list avalanche, before — jitter 10%) | 2026-10-07 | 500 rps, 500 pages warmed in burst, `CACHE_ITEM_TTL_S=20`, L1 off, `DB_MAX_CONNS=48`, ±10% jitter | — | — | — | 0% | **list avalanche reproduced** — 500 pages warmed in ~5s share near-identical TTL. Steady state between boundaries: **0/s**. At each TTL boundary (t≈20, t≈40): seq_scans spike to **~165/s peak** (vs 0/s valleys). Total **3,129 seq_scans** / 52s. Each is a `count(*)` on 600k rows — expensive work amplified by synchronized expiry | — (cure = more jitter) |
| 5 (list avalanche, after — jitter 50%) | 2026-10-07 | same regime, `CACHE_TTL_JITTER_PCT=50` | — | — | — | 0% | — | **±50% jitter flattens it**: re-expiry burst gone — smooth **~60–95/s band** from t≈20 onward, no spike, no 0/s valleys. Peak 181/s is the one-time warm/cold-fill (t≈2–5), not a re-expiry spike. Total **3,333** ≈ baseline (zero extra DB work — jitter only *moves* expiries, does not add refreshes). At production TTL=300s even ±10% already spreads 500 keys over 60s ≈ 8/s (insight #13). XFetch tested here and **rejected** — see note |

> **Caveat — every `4c-*` before-number is `DB_MAX_CONNS=1` *simulated* contention, not a healthy pool.** At the realistic `DB_MAX_CONNS=48` these herds/stampedes are absorbed by idle connections (sub-ms, no latency impact — insight #3/#8), so the 4c fixes are **insurance for the contended regime** (shared/pgbouncer-capped Postgres at 10K+ / cloud), not wins observed at a healthy pool on this box.
>
> **Gate rule for all 4c tasks: measure DB read count (`pg_stat_user_tables.idx_scan` delta), not tail latency.** Every 4c failure (avalanche, stampede, penetration, boundary expiry) is a DB-*work* problem: wasted index scans that scale with RPS and key count. Latency is a lagging symptom that a healthy pool absorbs — it masks the problem at 48 conns and only surfaces once the pool is contended (insights #9, #12). The success metric for every 4c before/after pair is `idx_scan spike → flat (~0)`, not `p95 < budget`.

<sub>Rung 2: seeded 100k rows (`make load-seed`). EXPLAIN showed the only Seq Scan was the exact count; **no index fixes a count where `is_active=true` matches ~all rows** (covering `(is_active,id)` index still Seq-Scanned). Fix was query-design: swap exact count for an O(1) `reltuples` estimate on the list path (exact `CountItems` kept for off-hot-path callers), plus a partial `(id) WHERE is_active=true` index for the active-ordered scan as soft-deletes accumulate. p95 20.68ms → 3.00ms (repo p95 19ms → 1ms). Target p95 < 15ms met; no Seq Scan on the hot path.</sub>

<sub>Rung 4: cache-aside re-introduced from `phase5-built` (`internal/valkey` + `internal/resilience/cache`, both fail-open). **Unconstrained 1K rps needs no cache** — the box holds p95~3ms with none (matches the ROADMAP: 1→1K is one stateless box). To earn the cache, the DB-contention regime that appears at 10K+/cloud was simulated locally with `DB_MAX_CONNS=1` (shared-Postgres / PgBouncer cap). Hot-read scenario `loadtest/cache_read.js` (`make load-cache`) hammers `GET /items/:id` over a 1000-key working set. Cache OFF (Valkey stopped → every op fails open to a miss → all reads queue on the one connection): p95=278.7ms, ~2.6K rps, **0 5xx (fail-open proven, T20)**. Cache ON (Valkey up → hits bypass the pool, repo p95→0ms): p95=4.63ms, 4.4K rps. 60× p95 improvement; target p95 < 50ms met. T29 (real 10K push) needs a second k6 machine.</sub>

<sub>Rung 4b: single-tier fail-open has a failure mode 4a didn't test — a Valkey outage **mid-load**. New scenario `loadtest/cache_resilience.js` (`make load-cache-resilience`) runs a steady 2000 rps split into warm→outage→recovery phases, dropping Valkey via `docker stop` at the warm→outage boundary and restoring it at outage→recovery, with per-phase p95 + a `db_reads` counter (repo_ms>0). **Before (single-tier):** outage p95 268ms, db_reads 221/s (DB flood — every miss hits the one connection), throughput collapsed. **After (tiered L1→L2→DB + self-healing breaker, T22a/T22b):** a bounded in-process LRU (`CACHE_L1_MAX`, `CACHE_L1_TTL_S`) absorbs the working set so outage db_reads → **0**; an in-memory circuit breaker around Valkey OPENs after `CACHE_BREAKER_FAIL_THRESHOLD` failures and skips the dead backend (no dial), HALF_OPEN-probing every `CACHE_BREAKER_RECOVERY_S` to self-heal on restart. Outage p95 268ms → **0.91ms (294×)**, 0 5xx, full 180k reqs. In-memory breaker (not Valkey-backed) by design — a breaker whose state needs Valkey is useless when Valkey is down. ADR 0007. L1 TTL set to 120s for the proof so L1 holds across the 30s outage (default 30s). Hardening (jitter/singleflight/bloom/hot-key, T23–T28a) remains symptom-gated.</sub>

<sub>Rung 4b — pool-size sweep + the unmasked dial tax. Re-ran the same scenario at the realistic `DB_MAX_CONNS=48`: outage p95 only **1.02ms** (tiered) and ~0.9ms even single-tier — 48 idle connections absorb the DB flood, so a Valkey outage is already mild on latency at a healthy pool. **The 294× headline is a low-pool effect** (it needs the DB pool to be the scarce resource, i.e. a shared/pgbouncer-capped Postgres at 10K+) — consistent with insight #3. What the fix still buys at 48 conns: DB load stays flat during the outage, and it removes the **per-cold-read dial tax**. A single-request micro-test (Valkey down, one cold `GET /items/:id`) isolates it: **L1 off → 3577ms** (go-redis hangs ~3.5s dialing the dead backend before the breaker opens); **L1 on → 0ms** (served from L1). The loaded k6 *masks* this — under 2000 rps the breaker trips in the first ~3.5s window, then the DB path (queued at 1 conn / sub-ms at 48) dominates — which is why the dial tax shows up starkly only in the micro-test. **Measurement caveat:** the `db_reads` counter keys on `Server-Timing repo;dur>0`, and that header is **integer-ms**, so sub-ms DB reads (what 48 idle conns deliver) round to 0 and are undercounted — `db_reads` is reliable only under real contention (`DB_MAX_CONNS=1`, reads ≫1ms, e.g. the 19,894 above); at 48 conns trust latency, not the counter. **Follow-up (not in this change):** cap go-redis `DialTimeout` (~200ms) in `internal/valkey/client.go` so the breaker trips faster and the outage-onset spike is bounded even before it OPENs.</sub>

<sub>Rung 4c — T23 (avalanche proof, no fix yet). New scenario `loadtest/cache_avalanche.js` (`make load-cache-avalanche`) warms every key in one burst (`shared-iterations`, so they share an absolute expiry), then reads steadily across the TTL boundary. **DB-read volume is measured from Valkey `keyspace_misses`, not the k6 `db_reads` counter** — the 4b caveat above proved `Server-Timing repo;dur` rounds sub-ms PK reads to 0, so that detector is blind to a fast herd; with L1 off every L2 miss is exactly one DB read, so the per-second `keyspace_misses` delta (polled by the make target) is the exact DB-read rate. Run with `CACHE_ITEM_TTL_S=20 CACHE_L1_ENABLED=false` (short TTL lands the avalanche in-window; L1 off so its own TTL can't mask the L2 expiry). **Result:** flat 0/s baseline for ~15s, then at the synchronized boundary `dbsize` craters 1000→313 and DB reads spike to **356/s** (a weaker echo follows as the re-cached — still identical-TTL — keys expire together again). This is the thundering herd the ladder predicted; T24 adds ±jitter to every `Set` (L1 + L2) and must flatten the spike into a smear. Latency does not move on this box (a PK read is sub-ms even under the burst) — the avalanche is a DB-load problem, not a tail-latency one, which is itself the lesson: pick the instrument (miss count) that matches the symptom.</sub>

<sub>Rung 4c — T24 (avalanche cure: ±jitter). `jitteredTTL(base, pct)` (`internal/resilience/cache/jitter.go`, `math/rand/v2`) spreads the L2 `Set` TTL by ±`CACHE_TTL_JITTER_PCT` (default 10) at the single L2 write point (`valkeyCache.Set`), so burst-warmed keys no longer share one expiry instant. Re-ran the T23 scenario (same regime). **Dose-response** (peak DB reads/s · `dbsize` min): no jitter **356/s · 313** (1s cliff) → ±10% **250/s · 646** (~6s ramp) → ±50% **81/s · 926** (flat ~50/s hum, spike gone). The band is ±pct·TTL, so at the proof's short 20s TTL ±10% is only ±2s — partial smearing; at the production 300s TTL the same ±10% is a ±30s band (60s spread) and flattens completely. **Scope: L2 only** (confirmed). L1 (`expirable.LRU`) has a fixed construction-time TTL and ignores per-call ttl, so per-entry jitter would mean swapping the lib; and an L1-expiry wave falls through to the (now-jittered) L2 as an L2 *hit*, not a DB read — no DB herd — so the reproduced symptom is fully addressed at L2. Unit test `jitter_test.go` asserts the ±band + variation + no-op guards (pct=0, base=0→no expiry). Next symptom, T25: `singleflight` stampede (one hot key expiring mid-load → M duplicate DB reads).</sub>

<sub>Rung 4c — T25 (stampede proof, no fix yet). New scenario `loadtest/cache_stampede.js` (`make load-cache-stampede`) points all load at a single hot key (`HOT_ID`) at 3000 rps with a short 5s TTL, so the run sees ~6–8 expiry cycles; the make target polls Valkey `keyspace_misses`/s. **This is a different failure from the avalanche and it survives the T24 jitter fix on purpose** — jitter desyncs expiry *across* keys, but one key still has a single expiry instant, and at that instant every concurrent in-flight request misses and runs its own DB read before the first `Set` repopulates the cache. Result: each cycle is a burst of **M = 58–63 duplicate DB reads for one logical value** (0 between, all hits), where an ideal cache does exactly 1 (the rest wait on it). Run L1 off (else L1 serves the hot key and hides the L2 stampede) and `DB_MAX_CONNS=1` (widens the repopulation window so the pileup is observable — on a healthy pool the window is sub-ms and M shrinks, the dual of insight #3/#8: the stampede is contention-gated too). p95 stays 3.3ms, 0 5xx — like the avalanche this is a wasted-DB-work problem, not a latency one. T26 collapses concurrent same-key misses with `golang.org/x/sync/singleflight` → exactly 1 DB read per expiry; re-run must flatten each burst to 1.</sub>

<sub>Rung 4c — T26 (stampede cure: `singleflight`). `Service.Get` wraps the miss→`GetItem`→`Set` block in `s.sf.Do(key, …)` (`golang.org/x/sync/singleflight`, promoted from indirect): when a hot key expires, the first goroutine does the DB read + repopulate and all concurrent followers wait and share its result. Per-expiry DB reads **41 → 1** (peak), total **142 → 8** over the run (≈1 per expiry = ideal), 0 5xx, p95 flat at ~3.5ms. **Instrument correction — second occurrence of the T23 lesson.** The T25 proof polled Valkey `keyspace_misses`, which counts *cache* misses; singleflight collapses the *DB* reads **downstream** of the misses while the miss count is unchanged (every request still runs its own `cache.Get`). Measured at the cache, the fix looks like it does nothing (misses still ~350/run); measured at the DB (`pg_stat_user_tables.idx_scan` on `items`, one PK scan per `GetItem`) it's a 41→1 collapse. `loadtest/cache_stampede.js` + `make load-cache-stampede` now poll `idx_scan`, not `keyspace_misses` — measure the layer the symptom lives in. Deterministic regression: `TestSingleflight_CollapsesConcurrentMisses` (integration) uses a barrier cache that releases 50 callers simultaneously and asserts exactly one `Set` (= one DB load). Two ponytail caveats in the code: `Do` shares the leader's ctx (a cancelled leader fails its followers — fine for a sub-ms PK read; `DoChan` if the loader grows slow), and no in-closure cache re-check (singleflight already collapses the burst). **Ops gotcha:** the integration suite's `setupPool` `TRUNCATE`s `items`, destroying the 100k k6 seed — re-run `make load-seed N=100000` after `go test -tags=integration` before any cache k6 proof. Next symptom, T27: XFetch (probabilistic early recompute) — only if p99 still spikes at the TTL boundary after singleflight.</sub>

<sub>Rung 4c — T27 (XFetch) **CLOSED — not earned, with the correct gate (DB read count).** Gate corrected from p99 to `idx_scan` per the 4c rule. Rewrote `cache_boundary.js` as a multi-key scenario mirroring the avalanche structure (warm 1000 keys → read across the TTL boundary) but with all shipping defences ON (jitter + singleflight), plus an `idx_scan` poller in `make load-cache-boundary`. **Measured boundary spike:** at 20s TTL / ±10% jitter = 1000 reads in ~6s, peak 194/s (structural: 1000 keys × 1 read/key, spread by jitter, each collapsed by singleflight). At 60s TTL = same reads in ~16s, peak 149/s. At production 300s TTL, ±10% jitter = ±30s (60s band) → ~17 reads/s — essentially flat. **XFetch was built, tested, and reverted.** The formula (`remaining < delta * beta * -ln(rand)`) requires `delta` (recompute duration) to be a meaningful fraction of the TTL. A sub-ms PK read (~200µs) against a 20s TTL gives `delta/remaining ≈ 0.00001` — the threshold is ~1667× too small to trigger before actual expiry. A prototype confirmed: XFetch ON produced an identical boundary spike (peak 296/s, total 2505 vs 2321 OFF — actually *more* reads from the wasted early-refresh attempts at t=2-3). **XFetch is designed for expensive recomputes (100ms+); for fast PK reads, jitter (T24) + singleflight (T26) are sufficient.** The boundary spike is a structural minimum (each key must refresh once) that jitter already distributes proportionally to TTL. Code reverted (~80 lines). Lesson: the correct gate reveals the correct answer — the boundary spike IS the jitter window, and the jitter window scales with TTL. At production TTL it's already flat.</sub>

<sub>Rung 5 — list cache: version invalidation + avalanche cure. Three changes. (1) **Cache-aside + singleflight on `Service.List`** (same pattern as `Get`), tiered cache + TTL + jitter. (2) **Exact `count(*)`** replaces the `reltuples` estimate — the cache absorbs the cost (fires once per TTL per page, not per request); `CountItemsEstimate`, `RowQuerier`, and the dead `pool` field deleted. (3) **Version-key invalidation** fixes the staleness bug: without it, `Create`/`SoftDelete`/`Delete` left cached list pages stale (new/deleted items invisible up to TTL). The key is now `items:list:<ver>:<page>:<size>`; writes `INCR` a version counter so every pre-write list key orphans (expires by TTL — no `KEYS`/`SCAN`/per-page delete). The version is stored as a **raw Valkey `INCR`/`GET`, NOT through `cache.Cache`** — the tiered cache fronts L2 with a fixed-TTL L1 (`tiered.go`), so a version read through the cache would be stale up to the L1 TTL after a bump, defeating the fix. `rdb == nil` (Valkey off) falls back to an in-process `atomic.Int64` (single-instance; cross-instance invalidation needs Valkey). Fail-open throughout — a version read/bump error never 5xxs. Cost: one sub-ms Valkey `GET` per `List` (far cheaper than the `count(*)` it guards). Proven by `TestListVersionInvalidation_Integration` (Create → immediate re-List of the SAME page shows the new row; SoftDelete drops it). **Avalanche cure — jitter, not XFetch.** New scenario `loadtest/cache_list_avalanche.js` (`make load-cache-list-avalanche`) warms PAGES pages in a burst, reads across the TTL boundary, polls `pg_stat_user_tables.seq_scan`/s. At ±10% jitter: flat 0/s between boundaries, **~165/s spike** at each TTL edge (3,129 total). XFetch was built and tested here and **rejected** (insight #18): at beta=1 it is a no-op (`delta` = tens-of-ms count(*) ≪ 20s TTL → refresh nudged only a few ms early); at beta=200 it smooths the curve but **raises total DB work 70%** (3,129→5,319) by over-refreshing. Raising **jitter to ±50% flattened the burst to a smooth ~60–95/s band at zero extra total work** (3,333 ≈ baseline) — jitter moves expiries, it does not add refreshes. At production TTL=300s even ±10% spreads 500 keys over a 60s band ≈ 8/s (insight #13). XFetch code reverted; `CACHE_TTL_JITTER_PCT` is the avalanche knob.</sub>

<sub>Rung 3: `make load RPS=100 DURATION=30s`, same 100k rows, single local instance. 7501 reqs, 0 failed, 100% checks. pgxpool wait measured indirectly — `server_repo_ms` (which wraps query + connection acquire via `TrackRepo`) held at p95=1ms identical to rung 2, so a 10× load increase added no acquire latency. `DB_MAX_CONNS` left at the 48 default (4×NumCPU=12); no index or pool change earned. Target p95 < 30ms met. Local-box headroom for higher rungs documented in `docs/local-tuning.md`.</sub>

<sub>Rung 1 run: `k6 run loadtest/load.js -e RPS=1 -e DURATION=30s --summary-trend-stats="avg,min,med,p(90),p(95),p(99),max"`, local single instance + local Postgres, `OTEL_ENABLED`/`APILOG` not yet built. 75 reqs, 0 failed, all envelope/request_id checks passed. Per-layer from `Server-Timing`: `service`/`repo` p99=2ms. Target p95 < 10ms met. Note: k6's default summary omits p99 — pass `--summary-trend-stats` (or set `summaryTrendStats` in options) to capture it.</sub>

---

## T29 — two-machine cloud ladder (AWS, c6i.2xlarge)

Two `c6i.2xlarge` (8 vCPU, 16 GB) on the same VPC/AZ (ap-south-1), sub-ms
network RTT. `DB_MAX_CONNS=16` (2×CPU), all cache tiers on, bloom on, 100k seed.

| Role | Instance | Private IP | Specs |
|---|---|---|---|
| API + Postgres (Docker) + Valkey (Docker) | `c6i.2xlarge` | 172.31.22.54 | 8 vCPU, 16 GB, Amazon Linux 2023, Go 1.24 |
| k6 load generator | `c6i.2xlarge` | 172.31.17.152 | 8 vCPU, 16 GB, Amazon Linux 2023, k6 v0.56 |

### WiFi attempt (failed, for the record)

Two local machines over WiFi (192.168.1.x). Hit the WiFi router's connection-
tracking limit at ~5K rps — `connection forcibly closed` / `dial tcp: connectex`
floods starting at ~8K VUs. Server was fine (`server_handler_ms` p95=3ms); the
router was the bottleneck. Not a valid server measurement.

### Cloud ladder — warm cache

| RPS (target) | achieved rps | p50 | p95 | p99 | max | err% | dropped/s | repo p95 | VUs used |
|---|---|---|---|---|---|---|---|---|---|
| 5,000 | 8,332 | 0.42ms | 0.66ms | 1.12ms | 58ms | 0% | 0 | 0ms | 5–10 |
| 10,000 | 16,665 | 3.7ms | 108ms | 120ms | 222ms | 0% | 0 | 104ms | 200–1335 |
| 15,000 | 19,035 | 159ms | **931ms** ❌ | 1.13s | 1.47s | 0% | 2977 | 747ms | 2000–11,064 |

### Cloud — cold cache at 10K rps

Server restarted, Valkey flushed, L1 empty, bloom rebuilt from DB at boot.

| RPS (target) | achieved rps | p50 | p95 | p99 | max | err% | dropped/s | repo p95 |
|---|---|---|---|---|---|---|---|---|
| 10,000 | 16,664 | 5.31ms | 113ms | 127ms | 226ms | 0% | 0 | 107ms |

Cold start ≈ warm: the ramp (0→10K over ~10s) warms L1/L2 before RPS peaks. Bloom builds at boot from `SELECT id FROM items` (100k rows, <100ms). No cold-start penalty.

### Ceiling analysis — why 15K breaks

The bottleneck is the **`/items` list endpoint** hitting Postgres on every request (not cached). At 15K target rps, the `load.js` mix sends ~50% list requests = ~7.5K list/s through a 16-conn pool. `server_repo_ms` p95 jumped from 104ms (10K) to **747ms** (15K) — pure pool-queueing: 7.5K req/s ÷ 16 conns = ~470 concurrent-per-conn at Little's law, far above what 16 connections can serve without queueing.

The cached `GET /items/:id` is still sub-ms at 15K (median 0ms) — the cache tier is not the bottleneck. The problem is that **list is un-cacheable in the current design** (it's paginated, dynamic, and always hits the DB).

### What would fix 15K

1. **Cache the list endpoint** — short-TTL cache of the first few pages (hot pages). Most list traffic hits page 1. A 5s TTL serves ~37,500 page-1 requests from cache per cycle. Biggest single win.
2. **Increase DB_MAX_CONNS** — 16→32 doubles pool capacity. But this is a band-aid; the list query still scales linearly with RPS.
3. **Separate Postgres onto its own instance** — gives the DB dedicated CPU/RAM. The shared instance makes Go, Valkey, and Postgres compete for the same 8 vCPUs.
4. **Read replicas** — route list queries to a read replica. Unlimited horizontal read scale.
5. **Connection pooler (PgBouncer)** — multiplex 1000s of goroutine connections through 32 real Postgres connections. Eliminates the conn-limit ceiling.

The lazy fix is #1 (cache list page 1) — it's the same cache-aside pattern already proven for single-item reads, and it eliminates ~80% of the DB-bound list traffic.

---

## Rung 5 cloud retest — list cache vs the T29 15K ceiling (2026-10-09)

Same two `c6i.2xlarge` topology as T29 (API+PG+Valkey co-located, k6 on the second
box), `DB_MAX_CONNS=16`, 100k seed, all cache tiers on. One variable changed vs T29:
`Service.List` is now cached (cache-aside + singleflight + version-key invalidation).
Hypothesis: T29 broke at 15K because the un-cached list saturated the 16-conn pool
(`repo_ms` p95 747ms). Does the list cache lift it?

| run | target | achieved rps | p95 | repo_ms p95 | handler_ms p95 | bottleneck |
|---|---|---|---|---|---|---|
| T29 (no list cache) | 10K | 16,665 | 108ms | 104ms | — | DB pool (list) |
| T29 (no list cache) | 15K | 19,035 | **931ms** ❌ | **747ms** | — | DB pool (list) |
| list cache | 10K | 16,664 | **7.3ms** | **0ms** | 0ms | none (DB out of path) |
| list cache | 15K | 21,521 | 259ms ❌ | **0ms** | **52ms** | per-request version GET |
| list cache + in-proc version cache | 15K | 21,724 | 249ms ❌ | 0ms | **0ms** | **k6 generator (rig)** |
| …same server, lightened client (`ceiling.js`) | 15K | 24,994 | **7.39ms** ✓ | — | — | server idle — **true number** |
| …lightened client, push to ceiling | 20K | 32,897 | 276ms ❌ | — | — | **server CPU-saturated (real ceiling)** |

**Result: the T29 DB-pool ceiling is gone.** `repo_ms` collapsed 747ms → **0** — Postgres
is no longer touched on the hot path at 15K. The list cache did exactly what the T29
"what would fix 15K" note predicted.

**Two bottlenecks surfaced and moved, in order:**

1. **The version GET became the hot path.** With L1 absorbing all list *data* GETs, the
   only per-request Valkey op left was the raw `GET items:listver` we added for
   invalidation — ~10.7K round-trips/s at 21.5K rps, **relayed by docker-proxy** (box-1
   sampler: docker-proxy 20–68% CPU, valkey ~19%, box CPU idle → 2.2%). `handler_ms`/
   `service_ms` p95 = 52ms while `repo_ms` = 0. Fix: an in-process 1s cache in front of
   the version read (commit 6667974) — box-1 sampler after: valkey **2%**, docker-proxy
   gone from the top list, box idle 17–88%, `handler_ms`/`service_ms` p95 → **0**.

2. **The load generator is now the wall.** After the fix, `handler_ms` p95 = 0 and box 1
   is half-idle, yet k6 still reports p95 ≈ 249ms. Box-2 (k6) sampler: CPU idle → **0.0%**
   sustained. One 8-vCPU k6 box pegs at ~21.7K req/s parsing JSON (`res.json()`) + running
   envelope checks on every response, and its wall-clock latency inflates once saturated.
   **The server already clears 15K with ~0ms server-side time; we cannot stress it with one
   k6 box.** Added `loadtest/ceiling.js` (`make load-ceiling`) — `discardResponseBodies` +
   status-only checks — to strip the k6-side cost.

**Clean 15K number (lightened client).** Re-ran 15K with `ceiling.js`: p95 **7.39ms**
(vs 249ms with `load.js`), p99 18.4ms, 24,994 req/s, 0 errors, 0 drops — and k6 used only
**134 of 4000 VUs** (iterations averaging 3ms). That proves the 249ms was **100% rig
artifact**: same server, same target, the only change was not parsing JSON on the client.
The honest rung-5 15K figure is **p95 = 7.39ms** — i.e. T29's 931ms → 7.39ms, ~126×.
(A 30K attempt with `ceiling.js` hit the single k6 box's limits — VU-cap + 339K dropped
iterations, p95 1.18s at 34.9K req/s — so ~35K req/s is one lightened generator's ceiling,
not the server's. True server ceiling still needs a 2nd generator or a bigger k6 box.)

**True server ceiling found (lightened client + box-1 sampler).** Pushing `ceiling.js` to
20K target drove **32,897 req/s** at p95 276ms with **box 1 CPU-saturated** (idle → 0%,
`us` 80%, `si` 7–12%) — and **Postgres 0%, Valkey ~0–8%**. The 8 vCPUs now go to the **Go
app + network softirq**, not the DB or cache: per-request envelope serialization, gin +
middleware + request-id + per-request slog, and netpoll for 33K req/s / 66 MB/s. That is a
genuine CPU ceiling, not a cacheable bottleneck and not the rig. **Server ceiling ≈ 33K
req/s (~16.4K iters/s) on one 8-vCPU co-located box; clean p95 < 10ms up to ~25K req/s.**
Co-location is no longer the issue (PG/Valkey idle) — the app owns the cores. To go higher:
more cores, or shave per-request CPU (reuse buffers, sample/disable per-request logging under
load) — rung-6 territory.

**Status:** rung-5 goal met and exceeded. T29 died at 15K (DB pool, 931ms); now DB + cache
are both ~0% and the service is CPU-bound at **~33K req/s** on 8 vCPU, clean p95<10ms to
~25K. The read path has no DB/cache ceiling left — the next limit is raw app CPU.

---

## Full ladder — current code (all optimizations on)

### Local single-box (12 cores, `DB_MAX_CONNS=48`)

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

### Cloud two-machine (c6i.2xlarge, `DB_MAX_CONNS=16`)

| RPS (target) | achieved rps | p50 | p95 | p99 | max | err% | repo p95 |
|---|---|---|---|---|---|---|---|
| 5,000 | 8,332 | 0.42ms | 0.66ms | 1.12ms | 58ms | 0% | 0ms |
| 10,000 | 16,665 | 3.7ms | 108ms | 120ms | 222ms | 0% | 104ms |
| 15,000 | 19,035 | 159ms | 931ms ❌ | 1.13s | 1.47s | 0% | 747ms |

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

11. **Cache penetration is a different class of failure: absent keys, not expiring
    keys.** Avalanche (T23), stampede (T25), and L1/breaker (T22a/b) all assume
    the requested id *exists* — they protect cache *refresh*. Penetration is a
    flood of ids that *never* existed: nothing ever caches a hit, singleflight
    can't collapse distinct keys, and L1/L2 are inert. The DB absorbs 100% of the
    flood — **43,867 index scans in 30s (≈ served requests), peak 3,054/s** —
    scaling 1:1 with attacker RPS. An in-process bloom pre-filter (`MayExist` = a
    handful of hash + bit-test ops, zero alloc) answers "this id cannot exist"
    before any cache or DB touch: DB reads **→ 0** (total and peak), p95 369ms →
    3.5ms. The bloom's false positives (gap ids inside the range) fall through to
    the DB once; a negative-cache tombstone (`[]byte{0}`, 30s TTL) absorbs repeats.

12. **Latency can mask a real problem — measure the resource the attack amplifies,
    not the symptom the pool absorbs.** At `DB_MAX_CONNS=48` the penetration flood
    shows p95 **27ms** (well inside budget) — latency looks fine, dashboard green.
    But the DB still ate **87,685 wasted index scans** in 30s, peak 4,398/s. A
    healthy pool hides the latency; it does not hide the work. The correct success
    metric for penetration is `idx_scan delta → ~0`, not `p95 < budget`. If you
    only watch latency, you see the problem when the pool is contended — i.e. when
    it's already an incident. Generalizes insight #9: pick the metric that matches
    the *resource* under attack (DB read count), not the *user-visible symptom*
    (tail latency).

13. **XFetch requires `delta ≈ TTL` to work — it's a tool for expensive recomputes,
    not fast reads.** The XFetch formula (`remaining < delta * beta * -ln(rand)`)
    is driven by `delta` (recompute duration). A sub-ms PK read (~200µs) against a
    20s TTL gives `delta/TTL ≈ 0.00001` — the probabilistic threshold is ~1667×
    too small to trigger before actual expiry. XFetch was built, tested, and
    confirmed to be a no-op (identical boundary spike, actually slightly worse from
    wasted early-refresh overhead). **The correct answer was already in place:**
    jitter (T24) spreads the 1000-key boundary across a window proportional to TTL
    (±10% of 300s = 60s → ~17 reads/s), and singleflight (T26) collapses each key
    to 1 read. At production TTL the boundary is already flat. The wrong gate (p99)
    deferred T27; the correct gate (idx_scan) closed it. Revisit XFetch only if the
    recompute becomes expensive (aggregation, join, slow query).

14. **L1 is the hot-key defence by construction — key-splitting is for architectures
    without an in-process tier.** A single hot key at 3000 rps with L1 on produced
    **2 DB reads and 5 Valkey ops** over 30s — 89,993/90,000 requests served from a
    map lookup in process memory. The premise of T28a (Valkey connection saturation)
    cannot reproduce because L1 intercepts before L2. Key-splitting fans one key into
    N sub-keys across Valkey shards, but that technique addresses L2-level load that
    L1 has already eliminated. Revisit only in a multi-shard, L2-only architecture
    (rung-6 T62) where there is no in-process tier to absorb the hot key.

15. **WiFi is not a load-testing network.** 8000 VUs over a consumer WiFi router
    saturated the router's NAT table / connection tracking before the server noticed.
    `server_handler_ms` p95 = 3ms while k6 reported p95 = 220ms — pure network, not
    server. Two machines on the same WiFi cannot produce a clean test above ~5K rps.
    Same machines on an AWS VPC (sub-ms RTT) delivered 10K rps at p95 = 108ms. The
    test rig is always in the measurement; WiFi puts a ~5K ceiling on it.

16. **The ceiling at 15K is the un-cached list endpoint, not the cached read.** At
    15K rps, `GET /items/:id` (cached) stays at median 0ms — the L1→L2→DB tiered
    cache, bloom, and singleflight are all working. The bottleneck is `GET /items`
    (paginated list): every request hits Postgres, no caching. 50% of `load.js`
    traffic is list = ~7.5K list/s through 16 DB conns → pool queueing → `repo_ms`
    p95 jumps from 104ms (10K) to 747ms (15K). **The next rung's fix is caching the
    list endpoint** (short-TTL on hot pages) or scaling the DB tier (read replicas,
    PgBouncer, separate instance). The single-item cache path has no observable
    ceiling at this load.

17. **Cold cache is a non-event at ramp-up load patterns.** Cold-cache 10K rps
    (Valkey flushed, server restarted, L1 empty) produced p95 = 113ms ≈ warm p95 =
    108ms. The ramp (0→10K over ~10s) warms L1/L2 from the first requests; by peak
    RPS the working set is cached. The bloom rebuilds from `SELECT id FROM items`
    at boot (<100ms for 100k rows). Cold-start matters only under instant-spike
    scenarios (0→10K in <1s) — the ramp pattern used by `load.js` hides it. A spike
    test (`spike.js`) with a cold cache would expose it if needed.

18. **XFetch needs `delta ≈ TTL`; jitter needs neither — jitter wins the avalanche.**
    The list cache looked like XFetch's case (count(*) is a real seq scan, not a sub-ms
    PK read), so it was built and measured against the 500-page avalanche. It failed the
    same way as T27: `delta` (tens of ms) is still ~1000× smaller than a 20s TTL, so the
    trigger window `delta·beta·(−ln rand)` is tens of ms wide — at beta=1 it nudges the
    refresh a few ms early (**no-op**, peak 165→193/s). Cranking beta=200 to widen the
    window to seconds *did* smooth the curve but **raised total DB work 70%** (3,129→5,319
    seq_scans) by refreshing every key several times per natural TTL — the opposite of the
    goal (less DB work). **Jitter is the correct lever and it's free:** ±50% jitter
    flattened the boundary burst to a smooth ~60–95/s band at **3,333 total ≈ the ±10%
    baseline of 3,129** — because jitter only *moves* each key's single expiry, it never
    adds a refresh. XFetch trades a spike for more total work; jitter trades a spike for a
    wider window at constant work. The avalanche is a *when-they-expire* problem, and
    jitter controls exactly that (spread = ±pct·TTL) with no downside. XFetch is only worth
    its complexity when `delta` is a real fraction of the TTL (100ms+ recompute against a
    few-second TTL) so the window is meaningful AND the refresh genuinely avoids a re-miss.
    Neither held here. Reverted; `CACHE_TTL_JITTER_PCT` is the knob.

19. **Caching the list endpoint removed the exact T29 ceiling — the hypothesis-driven
    retest paid off.** T29 named the 15K wall precisely (un-cached list → 16-conn pool →
    `repo_ms` p95 747ms) and predicted "cache the list" as the fix. The cloud retest, one
    variable changed, confirmed it: 15K `repo_ms` **747ms → 0**, Postgres untouched on the
    hot path. Writing the ceiling analysis *with the predicted fix* at T29 turned the next
    rung into a one-line verification instead of a fresh investigation. A load-test result
    is worth more when it ends with the next experiment, not just the current number.

20. **The thing you add to make caching correct can become the hot path.** Version-key
    invalidation needs a per-request version read. Once L1 absorbed the list *data*, that
    auxiliary `GET items:listver` was the ONLY per-request Valkey op left — ~10.7K round-
    trips/s — and it, not the data, saturated the box (through docker-proxy). The fix was
    to memoize the aux lookup in-process (1s TTL) just as aggressively as the data. Lesson:
    when you cache the expensive thing, the next bottleneck is whatever you left un-cached
    on the same path — including your own correctness machinery. Also: **docker-proxy
    (Docker's userland port relay) is a real CPU cost at high rps** and a co-location
    artifact — publishing Valkey's port taxes the shared box; a separate/host-networked
    Valkey (or managed instance, as in prod) removes that hop.

21. **`handler_ms`=0 with `http_req_duration`=249ms means the bottleneck is outside the
    app — measure both ends.** After the version-cache fix the server processed each
    request in ~0ms server-side (handler/service/repo all p95=0) with the server box
    half-idle, yet k6 reported p95=249ms. The gap lives in the transport + the load
    generator: box-2 (k6) CPU idle → **0.0%**. One 8-vCPU k6 box tops out ~21.7K req/s
    because `res.json()` + per-response checks peg it, and a saturated generator inflates
    its own wall-clock latency. You cannot measure a server ceiling with a generator that
    is itself the ceiling. Sample CPU on BOTH boxes; the fix is a lighter client
    (`discardResponseBodies`, status-only checks — `loadtest/ceiling.js`) or a second
    generator, never a bigger server. Generalizes insights #5/#15: the rig is always in
    the measurement, and server-side timing (`handler_ms`) is how you prove it is the rig.

22. **Verify the new binary is actually serving before trusting a before/after — a
    `go run` child outlives `pkill`.** The first retest of the version-cache fix showed
    no change (p95 259→249ms) and nearly sent us down the wrong path. Cause: `go run
    ./cmd/server` spawns a **child** binary (`/tmp/go-build…/exe/server`) that holds the
    port; `pkill -f 'cmd/server'` matched the `go run` wrapper but not the child, so the
    **old binary kept serving** and the "fix" never deployed. The result was a clean,
    plausible, and completely meaningless before/after. Fix: kill by port
    (`fuser -k 8000/tcp`) or build an explicit binary (`go build -o /tmp/app`) so there is
    one process to manage, and confirm deployment out-of-band (`git rev-parse HEAD`, a
    boot log line, or a behaviour probe) before believing any A/B number. A perf result
    from an unverified deploy is worse than no result — it looks like evidence.

23. **A well-tuned read service bottoms out on app CPU, not the DB or cache — and that's
    the goal.** Once the list cache + version cache were in, pushing a lightened client to
    ~33K req/s saturated the server box's 8 vCPUs with **Postgres and Valkey both ~0%**.
    The CPU went to the Go app (`us` 80%) + network softirq (`si` 7–12%): per-request
    envelope serialization, gin/middleware/request-id, per-request slog, and netpoll at
    66 MB/s. Clean p95 < 10ms held to ~25K req/s. This is the healthy end state — every
    cacheable/DB bottleneck has been pushed off the hot path (T29's DB pool → 0, the
    version GET → memoized), so the only thing left to saturate is raw compute. The ceiling
    moved from "16 DB connections" (T29, fixable with a cache) to "8 CPU cores" (fixable
    only with more cores or less per-request work). Knowing *which* resource binds tells you
    the next lever: it's now horizontal scale or per-request CPU shaving (buffer reuse,
    sampled logging), not another cache. Co-location stopped mattering once PG/Valkey went
    idle — the app already owns the cores.
