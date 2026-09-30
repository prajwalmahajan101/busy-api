# PRD — busy-api: A Database-Backed API Built to Scale 1 → 100K req/s

**Status:** Draft
**Owner:** prjawal
**Last updated:** 2026-09-03

---

## 1. Summary

Build a simple, correct HTTP API that reads data from a database and returns
it — then systematically scale it from **1 req/s to 100,000 req/s**, learning
and proving each scaling technique at the cheapest possible feedback loop
(local first, cloud only when the architecture itself is what's under test).

This is as much a **learning/benchmarking project** as a product: the goal is
to internalize where an API bends under load and which fix each bottleneck
demands, with hard numbers at every stage.

---

## 2. Goals

- G1. Ship a correct, layered, DB-backed read API (Go + PostgreSQL).
- G2. Establish a repeatable **benchmark harness** (k6) that reports
  p50/p95/p99 + throughput + error rate at every stage.
- G3. Climb the scaling ladder (1 → 10 → 100 → 1K → 10K → 100K req/s),
  applying exactly one fix per bottleneck and proving it with a before/after
  measurement.
- G4. Prove rungs 1–4 **locally** (≤ 10K req/s) before spending on cloud.
- G5. Keep cloud test spend trivial and predictable (< $5 for a full day of
  10K req/s testing).

## 3. Non-Goals

- Not building auth, multi-tenancy, or a write-heavy/transactional workload
  in v1 (read path first; writes come later).
- Not a production product with an SLA — this is a scaling testbed.
- Not adopting Kubernetes/service mesh before rung 5 demands it.
- Not standing up full observability (Grafana/OTel) before the single-binary
  stage goes blind (~rung 4–5).

---

## 4. Guiding Principle

> At every scale tier, the bottleneck is almost never the API code — it's the
> **database** and the **connection/network layer**. "Scaling the API" is 80%
> about how we talk to the DB and how we cache, not about the language.

Corollary: **earn the complexity.** Build, wire, and prove each layer (cache,
replica, PgBouncer, sharding, observability) exactly at the rung its bottleneck
appears — never before. "Built" is not "earned": a subsystem that exists but is
not on the hot path with a benchmark proving it belongs there is debt, not
progress. The one thing built *first* is the benchmark harness, because every
rung needs a baseline to measure against.

**Restart note (2026-09-30):** the earlier build ran ahead of this rule —
Valkey resilience and the outbound httpclient were built before any load test
existed. Mainline was recut from the pre-Valkey baseline (`7e9b706`); that code
is preserved in tag `phase5-built` and re-introduced at its rung, each behind a
k6 run that proves the failure first and the fix second.

---

## 5. Tech Stack (decided)

| Concern | Choice | Rationale |
|---|---|---|
| Language | **Go** | Best throughput-to-effort ratio; single binary; cheap concurrency; comfortably 50–100K req/s per node. |
| DB | **PostgreSQL** | Battle-tested relational store; clean scaling story (indexes → replicas → PgBouncer → sharding). |
| DB driver/pool | `jackc/pgx/v5/pgxpool` | Fastest Go PG driver with native pooling. |
| Router | `net/http` (Go 1.22+) or `chi` | stdlib is sufficient; chi for middleware ergonomics. |
| Migrations | `golang-migrate` | Versioned SQL up/down. |
| Cache (rung 4+) | **Redis** | Cache-aside for hot reads. |
| Load testing | **k6** | Scriptable ramp tests; native p95/p99; threshold pass/fail gates. |
| Logging | `slog` (stdlib) | Structured JSON + correlation IDs from line one. |
| Observability (rung 4–5) | OpenTelemetry → Prometheus + Grafana | Bolt-on later; not day-one. |

---

## 6. Architecture (v1)

Layered, stateless, swappable:

```
cmd/api/main.go        # wire config, pool, router, server; graceful shutdown
internal/
  config/              # env parsing (host, port, DB URL, pool size)
  handler/             # HTTP layer — thin, no business logic
  service/             # business logic (cache-aside lives here later)
  repository/          # DB queries only, swappable
  model/               # domain structs
migrations/            # .sql (golang-migrate)
docker-compose.yml     # local Postgres (+ Redis at rung 4)
Makefile               # run, test, migrate, loadtest
```

**Non-negotiables (get these right in v1 so every rung is additive):**

1. One `pgxpool`, created once in `main`, injected down. Never a connection
   per request. Start `MaxConns ≈ 4 × NumCPU`, then measure.
2. Context deadline on every DB call (`context.WithTimeout`, ~2s).
3. Parameterized queries only (`$1` placeholders) — no string concat.
4. Handlers stay thin: decode → service → encode.
5. `/healthz` that pings the pool + structured JSON logging with a
   request/correlation ID.
6. Graceful shutdown (`http.Server.Shutdown`) on SIGTERM.
7. Stateless — no in-memory session state — so N instances scale horizontally.

---

## 7. The Scaling Ladder (functional requirements)

Each rung: what breaks first, the fix, the p95 target for a DB-backed read.

| Rung | Load | Bottleneck that appears | Fix to apply | p95 target |
|---|---|---|---|---|
| 1 | 1 req/s | none — correctness only | Build it right (§6) | < 10ms |
| 2 | 10 req/s | Seq Scan | Add indexes on filtered/joined columns | < 15ms |
| 3 | 100 req/s | pool sizing / slow queries | Tune pool; `EXPLAIN ANALYZE` hot queries | < 30ms |
| 4 | 1K req/s | DB read pressure | **Redis cache-aside + read replica** | < 50ms |
| 5 | 10K req/s | single node + raw PG conns | **N instances + LB + PgBouncer** | < 100ms |
| 6 | 100K req/s | single DB + origin load | **Shard/partition + CDN/edge cache** | < 200ms |

Latency is *allowed* to rise with load — success = staying **under the p95
budget** (per global backend defaults: p95 < 200ms for user-facing endpoints).

### Cache hardening sub-ladder (rung 4→6, symptom-gated)

Once cache-aside is on the hot read (rung 4), each hardening technique is
earned by **reproducing its failure in k6 first, then proving the fix** —
before/after in the logbook. Never adopt preemptively.

| Technique | Failure it fixes | Rung |
|---|---|---|
| TTL jitter | avalanche — synchronized key expiry ⇒ DB spike | 4 |
| `singleflight` | stampede — hot key expiry ⇒ M duplicate DB reads | 4 |
| probabilistic early expiration (XFetch) | stampede tail at the TTL boundary | 4–5 |
| bloom filter | penetration — non-existent-key lookups skip cache ⇒ DB | 5 |
| hot-key protection | one key saturates a single cache shard | 5–6 |

Order within rung 4: TTL jitter → `singleflight` → XFetch.

### Not on the RPS ladder — outbound resilience

Circuit breaker, retry, and the outbound httpclient (auth, error mapping, SSRF
guard) protect **outbound** calls only. An inbound read path with no upstream
pays zero for them. They are **consumer-driven** — wired when a real upstream
lands, at no particular rung. Rate-limit throttle is protective, not a
throughput lever; it enters at rung 5 as edge hardening.

---

## 8. Benchmarking Requirements

- **Metrics:** p50, p95, p99, max, throughput (req/s), error rate. Report
  latency and throughput **as a pair** — never one without the other.
- **Method:** k6 **ramp test** — increase VUs in stages and find the "knee"
  (where p99 spikes while throughput flattens). The knee is the current ceiling.
- **Gates:** k6 `thresholds` (`p(95)<200`, `p(99)<500`, `http_req_failed<0.01`)
  so each run is pass/fail, not eyeballed.
- **Layer attribution:** `slog` timing per layer (handler / service / repo)
  with a correlation ID, so a slow p95 points at the responsible layer.
  Pair with `EXPLAIN ANALYZE` for the query internals.
- **Logbook:** maintain a before/after table per rung:

  | Rung | Load | p50 | p95 | p99 | err% | Bottleneck | Fix applied |
  |---|---|---|---|---|---|---|---|

- **Per-rung loop:** ramp → find knee → read layer split → apply one fix →
  re-run identical script → compare diff.

---

## 9. Local Testing Scope

**Reliable local zone:** everything up to ~10K req/s (covers rungs 1–4).

| Setup | Believable ceiling |
|---|---|
| k6 + API + Postgres on one laptop | ~5K req/s (CPU contention above this) |
| k6 on a **second machine**, API + PG on laptop | ~10K req/s (clean) |
| Fully tuned (FDs, ports, PG resourced), k6 remote | ~50K req/s (noisy) |
| 100K req/s | Not meaningful locally — needs distributed load gen |

**What runs out first (never the Go code):** ephemeral ports → file
descriptors (`ulimit -n 65535`) → CPU contention → raw Postgres connections.

**Rule:** if the API bends before 5–10K locally, cloud won't save it — fix it
here first. If it holds at 10K, rungs 1–4 are validated for free.

---

## 10. Cloud Migration Trigger

Move to cloud when **any** of these is true (not before):

1. Need a **read replica** or managed Postgres (~rung 4, ~1K req/s).
2. Need **more than one API instance** behind a load balancer (~rung 5, ~10K).
3. Need **multi-AZ / HA** (single-machine failure unacceptable).
4. Load test needs **distributed generation** to be believable (~50K+ req/s).

---

## 11. Cloud Cost Model (10K req/s test on AWS, us-east-1, on-demand)

Load tests run for **minutes**, so compute is cheap. The trap is
**data-transfer-out** if the load generator sits outside AWS.

**Recommended — k6 on EC2 in the same region:**

| Component | Instance | ~Hourly |
|---|---|---|
| API server | `c7g.xlarge` (ARM) | $0.14 |
| Postgres on EC2 | `c6i.large` | $0.09 |
| Load gen (k6) | `c6i.2xlarge` | $0.34 |
| Data transfer (same region) | — | ~$0 |
| **Total** | | **~$0.57/hr** |

- Single 10K req/s test: **cents**.
- Full day iterating (~4 active hrs): **~$2–3**.

**Trap — k6 on your laptop:** 10K req/s × 1KB = 36 GB/hr egress ×
$0.09/GB ≈ **$3.24/hr just for data leaving AWS** — more than all compute.

**Cost controls:** run k6 in-region; use Graviton; Spot for the throwaway
load-gen box; script start→test→teardown; set a $10 billing alarm. The real
risk isn't the test — it's **forgetting to stop idle instances** (~$400/mo).

---

## 12. Milestones

- **M1 — Correct API (rung 1):** healthz + pool + one read endpoint;
  migrations; `EXPLAIN`-clean query. Local.
- **M2 — Benchmark harness:** k6 ramp script + thresholds + `slog` timing
  middleware; logbook table started.
- **M3 — Rungs 2–3:** indexes + pool tuning; prove < 30ms p95 @ 100 req/s.
- **M4 — Rung 4:** Redis cache-aside + read replica; prove climb to 1K req/s;
  push local ramp toward 10K (k6 on second machine).
- **M5 — Cloud rung 5:** EC2 deploy + LB + PgBouncer + multi-instance; first
  in-cloud 10K req/s test; add Prometheus + Grafana.
- **M6 — Rung 6 (stretch):** shard/partition + CDN/edge; architecture-level
  100K req/s.

---

## 13. Success Metrics

- Rungs 1–4 proven locally with logbook evidence (before/after per fix).
- Each rung meets its p95 target at its target load with < 1% errors.
- A full day of 10K req/s cloud testing costs < $5.
- Every scaling fix is additive — the v1 core is never rewritten.

---

## 14. Open Questions

- Exact domain/data model for the read endpoint (what "items" are).
- Distributed DB choice at rung 6 (Citus/CockroachDB/Vitess/DynamoDB) —
  depends on access pattern; decide when rung 5 data is in.
- Whether writes enter scope after the read path is proven.
