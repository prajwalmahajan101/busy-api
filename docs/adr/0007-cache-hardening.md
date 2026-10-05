# 0007 — Cache hardening: tiered L1→L2→DB + self-healing Valkey breaker

**Status:** Accepted
**Date:** 2026-10-05
**Deciders:** prjawal

---

## Context

Rung 4a put a single-tier Valkey cache-aside on the hot read (`items.Get`),
proven under DB contention (`DB_MAX_CONNS=1`): p95 278ms → 4.6ms. But the k6
resilience scenario (`loadtest/cache_resilience.js`) exposed what happens when
Valkey drops **mid-load**: outage p95 jumped 2ms → **268ms** and the DB read-rate
jumped 7/s → **221/s** (19,894 of 21,070 DB reads landed in the 30s outage
window). Fail-open kept availability (0 5xx), but the naive single-tier cache
turns a Valkey outage into a DB flood — every miss falls through to the one scarce
connection — and, on an unreachable (not refused) Valkey, adds a per-request dial
tax on top.

This is the failure the rung-4b tasks (T22a, T22b) exist to fix, and it was
reproduced on current code before any change, per the rung-4b enforcement rule.

## Decision

Harden the cache as **composable decorators** over the existing `cache.Cache`
interface, so the service layer is untouched. The hot-read cache becomes:

```
tieredCache{ l1: lruCache, l2: breakerCache{ inner: valkeyCache, br: memoryBreaker } }
```

1. **L1 in-process tier (T22a).** A bounded LRU with a short TTL
   (`hashicorp/golang-lru/v2/expirable`) fronts Valkey: read L1 → L2 → DB,
   populate L1 on the way back. During a Valkey outage the working set is served
   from L1, so the DB read-rate stays flat instead of flooding. L1 is bounded by
   `CACHE_L1_MAX` (LRU eviction) so it cannot OOM when the whole working set is
   pushed through it.

2. **Self-healing breaker around L2 (T22b).** An **in-process** circuit breaker
   (`internal/resilience/breaker`, memory impl) guards every Valkey op. A backend
   error is mapped to a breaker-tripping `TransientError`; after
   `CACHE_BREAKER_FAIL_THRESHOLD` failures the breaker OPENs and ops return
   without calling Valkey — no dial. A HALF_OPEN probe every
   `CACHE_BREAKER_RECOVERY_S` re-closes it when Valkey returns.

3. **Fail-open moved to a decorator.** `valkeyCache` now propagates backend errors
   (so the breaker can count them); a `FailOpen` decorator degrades errors to a
   miss/no-op for the non-tiered path, and the breaker tier fails open on the
   tiered path. Availability is unchanged (F-6): a cache outage is never a 5xx.

### Why in-memory breaker (not the Valkey-backed variant)

A circuit breaker whose state lives in Valkey is useless when Valkey is the thing
that is down — and dialing Valkey to read breaker state is the very dial tax we
are removing. So only the in-memory breaker is re-introduced from `phase5-built`
at this rung; the cross-replica Valkey breaker lands with outbound resilience
(T64), where the guarded dependency is a separate upstream.

## Consequences

- A Valkey outage is absorbed by L1 (DB load stays flat) and the breaker stops
  dialing the dead backend; both auto-recover when Valkey returns (NFR-R5).
- L1 can serve slightly stale data for up to `CACHE_L1_TTL_S` (default 30s) — an
  accepted trade for outage absorption on a read API.
- Breaker state is per-process, not fleet-wide: each replica learns the outage
  independently (acceptable — the cost is one replica's threshold of dials).
- One new pinned dependency (`hashicorp/golang-lru/v2`), chosen over a hand-rolled
  LRU for correct, thread-safe eviction + TTL.

## Usage

Config (`internal/config`): `CACHE_L1_ENABLED`, `CACHE_L1_MAX`, `CACHE_L1_TTL_S`,
`CACHE_BREAKER_FAIL_THRESHOLD`, `CACHE_BREAKER_RECOVERY_S`. Wired in
`cmd/server/main.go` via `cache.NewTiered("items", rdb, cfg)`.

The remaining cache-hardening techniques — TTL jitter (avalanche), `singleflight`
(stampede), XFetch, bloom filter (penetration), hot-key splitting — are tracked
under this same ADR and added as future rows, **each earned by a reproduced k6
failure first** (rung-4c, T23–T28a).
