# 0001 — Go + PostgreSQL as the scaling-testbed stack

**Status:** Accepted
**Date:** 2026-09-03
**Deciders:** prjawal

---

## Context

busy-api is a database-backed read API whose explicit purpose is to be scaled
from 1 req/s to 100,000 req/s, proving each scaling technique with hard
benchmark numbers (see `PRD.md`). The stack choice must therefore optimize for:

- High single-node throughput (so we reach high rungs with few instances).
- A clean, well-understood scaling path for the datastore (indexes → cache →
  read replica → connection pooler → sharding).
- Fast local feedback so rungs 1–4 (≤ 10K req/s) can be proven on a laptop
  before any cloud spend.
- Low operational and cognitive overhead — this is a testbed, not a platform.

The governing insight for the whole project: **at every tier the bottleneck is
the database and the connection/network layer, not the API code.** The language
buys ~one tier of headroom; connection pooling and caching buy several. So the
stack must make the *DB path* easy to get right, not just be "fast."

Alternatives considered:

- **Rust (Axum):** highest throughput, but slower to write and steeper to
  iterate — the extra ceiling is wasted when the DB is the bottleneck anyway.
- **Node/TypeScript (Fastify):** good to ~10–30K req/s/node, great DX, but
  falls behind under CPU-bound load and adds an async-runtime footgun surface.
- **Python (FastAPI):** easiest to write, slowest runtime; would force
  horizontal scaling earlier and muddy per-node benchmark signal.
- **MySQL / a distributed DB up front:** rejected — starting distributed
  violates "earn the complexity" and hides the per-rung lessons.

## Decision

Use **Go + PostgreSQL** as the v1 stack.

- **Go** for the API: compiled, single static binary, cheap goroutine
  concurrency, strong stdlib HTTP, and comfortable 50–100K req/s per node.
- **PostgreSQL** for storage, accessed via **`jackc/pgx/v5/pgxpool`** (native
  pooling; not `database/sql`+`lib/pq`).
- Layered `handler → service → repository`, stateless, with a single injected
  pool, context deadlines on every DB call, and parameterized queries only.
- Additive scaling components introduced only at the rung that demands them:
  **Redis** (cache-aside, rung 4), **read replica** (rung 4), **PgBouncer**
  (rung 5), **shard/partition + CDN** (rung 6).
- Governing rule: **earn the complexity** — add each layer exactly when the
  previous one goes blind or bends, never preemptively.

## Consequences

**Positive**

- One binary + Docker Postgres makes local rungs 1–4 provable for ~$0.
- pgxpool + context deadlines make the two highest-value correctness
  properties (no per-request connections, no unbounded queries) the default.
- Postgres's scaling story maps 1:1 onto the ladder, so each rung teaches one
  discrete technique.
- Stateless Go instances scale horizontally with zero code change at rung 5.

**Negative / trade-offs**

- Go has a lower raw ceiling than Rust — accepted, because the DB caps us first.
- More boilerplate than FastAPI for the same endpoint — accepted for runtime
  headroom and clean per-node benchmark signal.
- Postgres single-primary write scaling is finite; rung 6 will require an
  explicit sharding/distributed-DB decision (deferred to a future ADR once
  rung-5 data exists).

**Neutral**

- Observability (OTel → Prometheus/Grafana) is deliberately deferred to
  rung 4–5; `slog` structured logging with correlation IDs is the day-one
  substitute and the bolt-on point.

## Usage

- New endpoints follow `handler → service → repository`; handlers stay thin,
  business logic lives in the service, SQL lives in the repository.
- Always use the injected `pgxpool` and a `context.WithTimeout` per DB call;
  never open a connection per request; parameterized queries only.
- When adding a scaling component (Redis, replica, PgBouncer, shard), confirm
  the current rung's bottleneck actually demands it (benchmark evidence in the
  PRD logbook) before introducing it.
- Revisit this ADR — or supersede it — before adopting a distributed DB
  (rung 6) or changing the language/datastore.
