# busy-api

A **Go + PostgreSQL read-API scaling testbed**. One small, honest service, scaled
from 1 to tens of thousands of requests per second — where every optimization is
**earned by a reproduced failure and proven with k6 numbers**, never added on faith.

The governing rule is *earn the complexity*: a technique (index, pool tune, cache
tier, bloom filter, singleflight, list cache) is built, wired onto the hot path,
and benchmarked **only at the rung its bottleneck actually appears** — with a
before/after row in [`docs/benchmark-logbook.md`](docs/benchmark-logbook.md).

## Proven so far

Single `c6i.2xlarge` (8 vCPU), read-heavy mix (cached `GET /items/:id` + list),
Postgres + Valkey co-located, 100k rows — cloud-measured:

| metric | result |
|---|---|
| Rung-5 target (p95 < 100 ms @ 10K rps) | **met at p95 7.3 ms** |
| Clean throughput (within p95 budget) | **~25K req/s @ p95 7.4 ms** |
| Saturation ceiling | **~33K req/s** — app-CPU-bound (Postgres 0%, Valkey ~0%) |
| vs. the pre-cache baseline (T29) | 15K p95 **931 ms → 7.4 ms**, ~126× |

The read path has **no DB or cache ceiling left** — it bottoms out on raw app CPU,
which is the healthy end state for a cached read service. Going further is
horizontal scale (see [ADR 0008](docs/adr/0008-rung5-list-cache-and-horizontal-scaling.md)).

> Not yet proven: 50K+, horizontal scale (ALB + replicas), write-heavy load, and
> the remaining rung-5 features (throttle, async audit, observability, lifecycle).
> This is a `0.x` testbed under active development — see the roadmap.

## What's inside

- **Layered CRUD** — `handler → service → repository` (`internal/items`), thin
  handlers, SQL in `sqlc`-generated queries, `pgxpool` with per-call timeouts.
- **Tiered cache-aside** — L1 in-process LRU → L2 Valkey (circuit-breaker guarded)
  → DB, all **fail-open** (a Valkey outage degrades latency, never availability).
- **Cache hardening, each earned by a reproduced failure** — TTL jitter (avalanche),
  `singleflight` (stampede), bloom filter + negative cache (penetration), and a
  versioned **list cache** with `INCR`-based invalidation.
- **Per-layer timing** — every response carries
  `Server-Timing: handler;dur=… service;dur=… repo;dur=…`, so latency is
  attributable per layer from the client side.
- **A full k6 harness** — smoke / load / stress / spike / soak plus one scenario
  per reproduced cache failure (`make load-*`).

## Stack

Go · Gin · pgx/v5 + sqlc · goose · Valkey (go-redis) · k6. Observability
(OpenTelemetry → Tempo/Prometheus/Loki/Grafana) is scaffolded for rung 5.

## Quick start

```bash
cp .env.example .env            # set DATABASE_URL
docker compose up -d            # Valkey (cache/resilience backend)
# Postgres: use your own, or run one:
#   docker run -d --name busy-api-postgres \
#     -e POSTGRES_USER=root -e POSTGRES_PASSWORD=root_password -e POSTGRES_DB=busyapi \
#     -p 5432:5432 postgres:16-alpine
make migrate-up                 # apply goose migrations
make run                        # boot API on :8000
curl -s localhost:8000/ping
```

## Reproduce a benchmark

```bash
make load-seed N=100000                 # seed rows
make load RPS=1000 DURATION=30s         # ramp load, p95 budget
make load-cache                         # rung-4 cache-aside proof
make load-cache-list-avalanche          # rung-5 list-avalanche proof
```

Each `make load-*` target maps to a logbook row; the comment on the target names
the server-side knobs (`CACHE_ITEM_TTL_S`, `DB_MAX_CONNS`, …) it expects. For the
two-machine cloud ladder see [`docs/cloud-loadtest.md`](docs/cloud-loadtest.md).

## Docs

- [`docs/benchmark-logbook.md`](docs/benchmark-logbook.md) — every rung's
  before/after numbers, plus the accumulated performance insights.
- [`docs/ROADMAP.md`](docs/ROADMAP.md) — the RPS ladder and milestone map.
- [`docs/REQUIREMENTS.md`](docs/REQUIREMENTS.md) — functional/non-functional source of truth.
- [`docs/TASKS.md`](docs/TASKS.md) — ladder-ordered task list, one commit each.
- [`docs/adr/`](docs/adr/) — the decisions behind the design.

## Contributing & security

See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md). Licensed
under the [MIT License](LICENSE).
