# 0008 — Rung 5: list-cache + version invalidation, and the move to horizontal scaling

**Status:** Accepted
**Date:** 2026-10-09
**Deciders:** prjawal

---

## Context

T29 (two-machine cloud ladder) found the service's first hard ceiling at 15K rps:
the **un-cached `/items` list endpoint** saturated the 16-connection pgx pool
(`server_repo_ms` p95 104ms → **747ms**, overall p95 931ms), while the cached
single-item read stayed sub-ms. T29's own analysis predicted the fix: **cache the
list endpoint** (same cache-aside already proven for `Get`).

Rung 5's throughput target (M5 exit) is **p95 < 100ms @ 10K rps in cloud**. The
list endpoint was the one thing standing between us and that number, so the
list-cache work was added to rung 5 (it was not in the original rung-5 scope).

Two design problems had to be solved for a correct, fast list cache:

1. **Staleness on writes.** A cached list page must reflect `Create`/`Delete`/
   `SoftDelete` promptly, but tracking which page an item lands on is impractical
   (an insert shifts every downstream page).
2. **Count cost.** The list total had been a `reltuples` *estimate* (rung 2) to
   avoid a `count(*)` seq scan on every request.

## Decision

**1. Cache the list with the existing tiered cache + singleflight.** `Service.List`
now uses the same `cache.Cache` (L1 LRU → L2 Valkey breaker → DB) and
`singleflight` as `Get`. Exact `count(*)` replaces the `reltuples` estimate — the
cache absorbs the cost (one recompute per TTL per page, not per request), so
accuracy wins. `CountItemsEstimate`, `RowQuerier`, and the dead `pool` field were
deleted.

**2. Version-key invalidation.** List keys are `items:list:<ver>:<page>:<size>`.
Writes `INCR` a version counter so every pre-write key orphans and expires by TTL
— no `KEYS`/`SCAN`/per-page delete. The counter is a **raw Valkey `INCR`/`GET`,
deliberately not routed through `cache.Cache`** (an L1-cached version would be
stale up to the L1 TTL after a bump, defeating invalidation). `rdb == nil` falls
back to an in-process `atomic.Int64`. A **1s in-process memo** fronts the version
`GET` so it is not a per-request Valkey round-trip (see Consequences). Fail-open
throughout.

**3. Avalanche cure is TTL jitter, not XFetch.** The synchronized-expiry burst is
handled by `CACHE_TTL_JITTER_PCT`. XFetch was built and rejected (see ADR-adjacent
logbook insight #18): `delta` ≪ TTL makes it a no-op at low beta and *more* total
DB work at high beta; jitter spreads expiries at zero extra work.

**4. Next rung is horizontal, not another cache.** With the list cached, the
cloud retest showed the service **CPU-bound at ~33K req/s on one 8-vCPU box, with
Postgres 0% and Valkey ~0–8%** — the Go app + network softirq own the cores. Every
DB/cache bottleneck has been pushed off the hot path. Therefore rung 6 scales
**horizontally**: an ALB (or nginx) in front of N stateless API instances. This
**requires externalizing Postgres and Valkey to shared instances** (the current
co-located layout cannot be cloned per node). L1 stays per-node; L2 Valkey and the
version counter are shared — so **cross-node list invalidation already works**
(a write on any node INCRs the shared counter; other nodes see it within the 1s
memo window). This is the single biggest reason the version counter lives in
Valkey rather than purely in-process.

## Consequences

**Positive**

- T29's 15K DB-pool ceiling is gone: 15K `repo_ms` 747ms → **0**; clean p95 **7.39ms**
  (lightened client), ~126× over T29's 931ms. Rung-5 10K target (p95<100ms) beaten
  at 7.3ms.
- The read path has no DB or cache ceiling left — it bottoms out on raw app CPU
  (~33K req/s / 8 vCPU), which is the healthy end state for a cached read service.
- The design is already multi-node-correct: shared version counter + per-node L1.

**Negative / trade-offs**

- The 1s version memo means a write on another node is visible within 1s
  (same-node writes are immediate). Accepted: bounded, documented; single-instance
  deployments see zero staleness.
- One extra Valkey `GET` per list request existed before the memo — it became the
  hot path at 15K+ (CPU via docker-proxy) and had to be memoized. Lesson:
  correctness machinery on the hot path needs the same caching discipline as data.
- Going past ~33K req/s now requires real infra (shared PG + Valkey + ALB +
  replicas) — more moving parts than a single box.

**Neutral**

- The full M5 milestone (throttle T30, async audit T31–39, observability T40–50,
  lifecycle T51–53, cloud deploy T54–58) remains open; only the throughput/scaling
  slice of rung 5 is closed here.

## Usage

- List reads: rely on the versioned key; never hand-invalidate list pages — a write
  path must call `bumpVersion` (already wired into Create/SoftDelete/Delete).
- Before building the horizontal fleet (rung 6), **earn each node**: provision
  shared PG + Valkey + 2 API nodes + ALB, prove ~2× throughput, then grow. Watch
  for the next bottleneck to move to **shared Valkey** (L1-miss traffic × N nodes)
  before Postgres (cache keeps it cold).
- Revisit this ADR when the DB-tier strategy for rung 6 is chosen (read replicas /
  PgBouncer / sharding) — that is its own decision (draft `0009-rung6-db-strategy`).
