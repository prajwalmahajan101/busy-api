# Changelog

All notable changes are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/). Per-rung benchmark numbers live in
[`docs/benchmark-logbook.md`](docs/benchmark-logbook.md).

## [Unreleased]

- Rung 6 (horizontal): externalize Postgres + Valkey, ALB + replicas (ADR 0008).
- Remaining M5: rate-limit throttle, async audit log, OpenTelemetry, lifecycle.

## [0.1.0] — 2026-10-09

First tagged release: rungs 1–5 (the throughput slice), each optimization earned
by a reproduced failure and proven with k6 numbers.

### Added

- **Rung 1** — correct layered CRUD API (`handler → service → repository`),
  per-layer `Server-Timing`, and the full k6 harness (smoke/load/stress/spike/soak).
- **Rung 2** — partial active index; query-design fix for the list count.
- **Rung 3** — pgxpool tuning (proven uncontended; no change earned).
- **Rung 4** — tiered cache-aside (L1 LRU → L2 Valkey breaker → DB, fail-open) on
  `GET /items/:id`, plus hardening each earned by a reproduced failure: TTL jitter
  (avalanche), `singleflight` (stampede), bloom filter + negative cache
  (penetration). See ADR 0007.
- **Rung 5** — versioned **list cache** with `INCR`-based invalidation and
  in-process version memo; exact `count(*)` behind the cache. See ADR 0008.
- k6 scenarios for every reproduced cache failure (`make load-*`), the
  `load-ceiling` throughput profile, and the two-machine cloud runbook
  (`docs/cloud-loadtest.md`).

### Performance (cloud, single c6i.2xlarge, read-heavy, 100k rows)

- Rung-5 target **met**: p95 **7.3 ms @ 10K rps** (budget < 100 ms).
- Clean to **~25K req/s** (p95 7.4 ms); saturation ceiling **~33K req/s**,
  app-CPU-bound (Postgres 0%, Valkey ~0%).
- vs. the pre-cache baseline: 15K p95 **931 ms → 7.4 ms** (~126×). The list cache
  eliminated the DB-pool ceiling; the read path now bottoms out on app CPU.

[Unreleased]: https://github.com/prajwalmahajan101/busy-api/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/prajwalmahajan101/busy-api/releases/tag/v0.1.0
