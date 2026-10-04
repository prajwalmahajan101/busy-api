# busy-api — Tasks (ladder-ordered)

Derived from `docs/ROADMAP.md` "Build order" and `docs/PRD.md` §7. Sequenced by
the **RPS scaling ladder**, not by subsystem phase. Governing rule: **earn the
complexity** — a subsystem is built, wired onto the hot path, and proven at the
rung its bottleneck appears, never before. The benchmark harness is built first
so every rung has a baseline.

Each task is scoped to **one file / one concern / one commit**. Prior Phase 4–5
code (Valkey resilience, httpclient) is preserved in tag `phase5-built` / branch
`legacy` and re-introduced at its rung, each behind a k6 run proving the failure
first and the fix second.

## Ladder → milestone map

| Rung | Load | Focus | Milestone |
|---|---|---|---|
| 1 | 1 rps | correct API (done) + benchmark harness + baseline | M1 + M2 |
| 2 | 10 rps | indexes | M3 |
| 3 | 100 rps | pool tuning | M3 |
| 4 | 1K rps | cache-aside + cache hardening | M4 |
| 5 | 10K rps | horizontal + observability + throttle + audit + lifecycle | M5 |
| 6 | 100K rps | shard + CDN + audit queue sink | M6 |
| — | n/a | outbound resilience (breaker/retry/httpclient) — consumer-driven | — |

Success at each rung = under its p95 budget, < 1% errors. If a 10× jump breaks,
bisect with a half-decade rung (500, 2K, 5K, 50K) before tuning. Every rung
records a before/after row in `docs/benchmark-logbook.md`.

Requirement coverage: F-1/F-2/F-4/F-5 done at baseline; F-3 → T8; F-6 → T13–T14;
F-7 → T44; F-8 → T31–T35; F-9 → T36; F-10 → T30. NFR-P/S/R/SEC/O/M mapped inline.

---

## Rung 1 — Correct API + Benchmark Harness · _M1, M2_

### 1a. Correctness — DONE at baseline (`7e9b706`)

- [x] Phase 0 — infra & scaffolding (compose valkey, `.env.example`, `internal/config` + secrets seam, `Makefile`, `.air.toml`, `.golangci.yml`).
- [x] Phase 1 — foundations (`internal/reqcontext`, `internal/response` envelope, `internal/errs` typed hierarchy + gin `Handler`).
- [x] Phase 2 — HTTP edge middleware (Recovery, BodyLimit, CORS, SecurityHeaders, RequestID, RequestLogging, RateLimitHeaders); `main.go` wiring-only.
- [x] Phase 3 — persistence (`sqlc.yaml`, base migration, `internal/db` pool + `Atomic`, sqlc queries, generic helpers, `items` CRUD route, integration test).

### 1b. Per-layer timing (fixes review ISSUE-001)

- [x] T1. `internal/reqcontext` — add a per-request timing accumulator (context-stored struct with `AddServiceMS`/`AddRepoMS` or start/stop helpers); no new external deps (R21).
- [x] T2. `internal/store` (repo) — record repo-layer duration into the accumulator around each query call. _(done via `TrackRepo` in `items/service.go`)_
- [x] T3. `internal/items/service.go` — record service-layer duration into the accumulator.
- [x] T4. `middleware.RequestLogging` — read the accumulator; emit non-zero `handler_ms`, `service_ms`, `repo_ms` beside `request_id`. Unit test asserts all three populate on a CRUD call (R21).

### 1c. Benchmark harness

- [x] T5. `loadtest/lib/checks.js` — envelope-shape check + `request_id` header check helper.
- [x] T6. `loadtest/lib/thresholds.js` — shared threshold objects + per-layer timing extractor from response headers/body. _(per-layer timing exposed via a `Server-Timing` response header added in `middleware.RequestLogging`)_
- [x] T7. `loadtest/smoke.js` — 1 VU, 30s; hit each core route; fail on any non-2xx or missing envelope field.
- [x] T8. `loadtest/load.js` — `ramping-arrival-rate`; `RPS` env sets target; thresholds `http_req_duration p(95)<200ms`, `http_req_failed rate<0.01`; `--out experimental-prometheus-rw` (R20, R21).
- [x] T9. `loadtest/stress.js` — ramp past target until thresholds break; log break-point RPS/p95/err.
- [x] T10. `loadtest/spike.js` — 0 → peak → 0 burst; assert error rate stays under threshold during burst.
- [x] T11. `loadtest/soak.js` — 30 min sustained; assert goroutines/heap/`dropped_total` flat (no leak).
- [x] T12. Makefile targets `load-smoke`, `load`, `load-stress`, `load-spike`, `load-soak`, `load-matrix`; parameterize `BASE_URL`, `RPS`, `VUS`, `DURATION`.
- [x] T13. `docs/benchmark-logbook.md` — create with header `| Rung | Date | Load | p50 | p95 | p99 | err% | Bottleneck observed | Fix applied |`.
- [x] T14. Run `load.js RPS=1`; **record the rung-1 baseline row** (target p95 < 10ms) (R21). _(p50=1.89ms, p95=3.21ms, 0% err; per-layer service/repo med=1ms)_

**Exit (M1+M2):** `load.js RPS=1` passes thresholds; per-layer timing non-zero and attributable; logbook has the rung-1 baseline row.

---

## Rung 2 — Indexes (10 rps) · _M3 (partial)_

- [ ] T15. Run `load.js RPS=10`; `EXPLAIN ANALYZE` the hot read; capture the plan.
- [ ] T16. Add a goose migration for any missing index on a filtered/joined column showing a Seq Scan; re-run identical script; record before/after. p95 < 15ms (R22, F-3).

**Exit:** no Seq Scan on the hot path; p95 < 15ms @ 10 rps; logbook row.

---

## Rung 3 — Pool tuning (100 rps) · _M3_

- [ ] T17. Run `load.js RPS=100`; measure pgxpool wait time; tune `DB_MAX_CONNS` (start `4×NumCPU`); `EXPLAIN ANALYZE` remaining hot queries; add any missing index; confirm p95 < 30ms; record (R22).
- [ ] T18. `docs/local-tuning.md` — `ulimit -n 65535`, ephemeral-port range, two-machine k6 guidance for clean runs above ~5K rps (R23).

**Exit (M3):** p95 < 30ms @ 100 rps, < 1% errors; logbook rows for rungs 2, 3.

---

## Rung 4 — Cache-aside + hardening (1K rps) · _M4_

### 4a. Cache-aside

- [ ] T19. Cherry-pick / re-introduce `internal/valkey` client from `phase5-built`; verify `Ping` + empty-URL ⇒ in-memory fallback.
- [ ] T20. Cherry-pick / re-introduce `internal/resilience/cache` (memory + Valkey, fail-open) from `phase5-built`; integration test: Valkey down ⇒ DB result, never 5xx (R6, F-6).
- [ ] T21. Wire cache-aside into the hot read (`items` Get): Valkey → miss → Postgres → populate; invalidate on write/soft-delete.
- [ ] T22. Run `load.js RPS=1000`; confirm p95 < 50ms; record bottleneck + fix (R6).

### 4b. Cache hardening — one lesson per reproduced symptom

Each: k6 scenario proving the failure first, then the cure. Before/after in logbook. Order: TTL jitter → singleflight → XFetch.

- [ ] T23. **TTL jitter (avalanche):** k6 scenario — warm N keys, identical TTL, capture the synchronized-expiry DB spike.
- [ ] T24. Fix avalanche — add ±jitter to every cache `Set` TTL; re-run; prove the spike flattens.
- [ ] T25. **`singleflight` (stampede):** k6 scenario — hammer one hot key, expire it mid-load, count M duplicate DB reads.
- [ ] T26. Fix stampede — collapse concurrent misses per key via `golang.org/x/sync/singleflight`; re-run; prove 1 DB query per expiry.
- [ ] T27. **XFetch (early expiration):** only if p99 still spikes at the TTL boundary after T26 — recompute a hot value before expiry (age + β·log(rand)·delta); prove the boundary spike disappears.
- [ ] T28. **Bloom filter (penetration):** k6 scenario — flood random non-existent ids, capture ~100% miss → DB; add a bloom filter of existing keys (Valkey `BF.*` or in-process) short-circuiting known-absent lookups; re-run; prove DB load stays flat.
- [ ] T29. Push local ramp toward 10K rps (k6 on second machine); record local ceiling + where CPU/port exhaustion appears; document as cloud-migration trigger (R23, R24).

**Exit (M4):** p95 < 50ms @ 1K rps; cache fail-open verified; each hardening technique has a paired failure/cure logbook row; local ceiling documented.

---

## Rung 5 — Horizontal + observability (10K rps) · _M5_

> Trigger: rung 4 passes AND a read replica or second instance is needed. No cloud before both true (R24).

### 5a. Rate-limit throttle (edge hardening)

- [ ] T30. Re-introduce `internal/resilience/throttle` (sliding window, Valkey + in-memory) from `phase5-built`; **bound the in-memory per-IP map (TTL eviction / capped LRU)** so Valkey-down cannot leak memory (fixes review ISSUE-002); wire per-route → 429 + `X-RateLimit-*`; integration test: limit exceeded → 429 + headers; Valkey down → fallback holds (R10, F-10, NFR-P4).

### 5b. Async API audit log

- [ ] T31. `migrations/000N_api_log.sql` — daily-partitioned `api_log` (columns per R8), indexes on `request_id`/`timestamp`/`status`, `DEFAULT` partition (R8).
- [ ] T32. `ensureAPILogPartitions(ctx, days)` — run at startup + daily: `CREATE TABLE … PARTITION OF … IF NOT EXISTS` for next N days; drop partitions older than `APILOG_TTL_DAYS` (R8).
- [ ] T33. `internal/apilog/record.go` — `Record` struct + `Direction` enum.
- [ ] T34. `internal/apilog/collector.go` — bounded `chan Record`, non-blocking `Enqueue` (select-default drop + atomic counter), `Dropped() uint64` (NFR-R4).
- [ ] T35. `internal/apilog/worker.go` — `Sink` interface; `APILOG_WORKERS` drain goroutines; flush on `APILOG_BATCH_SIZE` OR `APILOG_FLUSH_MS`; `Drain(timeout)`.
- [ ] T36. `internal/apilog/sink_postgres.go` — `pgx.CopyFrom` bulk insert on a dedicated small pool (NFR-P3).
- [ ] T37. `internal/apilog/sink_queue.go` — stub implementing `Sink` (log warning, return nil; real Kafka/NATS deferred).
- [ ] T38. `middleware.APILog(collector, cfg)` — build Record post-handler; deterministic 2xx sampling by hashing `request_id` at `APILOG_SAMPLE_2XX`, always log non-2xx; optional sanitized body capture (capped) reusing `internal/sanitize` from `phase5-built` (R8, NFR-SEC5).
- [ ] T39. Integration test — p95 unchanged audit ON vs OFF; forced-full channel increments `dropped_total` without latency spike; batch rows land via `CopyFrom` (R8, NFR-P2/P3).

### 5c. Observability

- [ ] T40. `internal/telemetry/telemetry.go` — `Init(ctx, cfg)` builds OTel `resource`, `TracerProvider` (OTLP gRPC batch, parent-based ratio sampler), `MeterProvider` (OTLP periodic reader); W3C `TraceContext`+`Baggage` propagators; `OTEL_ENABLED=false` ⇒ no-op providers, stdout logs only; returns `shutdown` func (R16, NFR-O1/O5).
- [ ] T41. Install `otelgin.Middleware` after `RequestID`; verify server span carries `request_id` and `http.server.request.duration` populates (R16, NFR-O2).
- [ ] T42. `otelslog` bridge → Loki; slog `ReplaceAttr` injects `trace_id`/`span_id` into every line (R16, NFR-O3).
- [ ] T43. Custom instruments via `telemetry.Meter()` — `cache.hits`/`misses`, `throttle.rejected_total`, `apilog.dropped_total`, `breaker.state` (when outbound live); poll `pgxpool.Stat()` into gauges (R16, NFR-O2).
- [ ] T44. Exemplars — attach `trace_id` to the request-duration histogram for metric→trace jumps (R16, NFR-O4).
- [ ] T45. `deploy/observability/otel-collector-config.yaml` — receivers otlp (grpc/http); processors batch/memory_limiter/resource; exporters otlp/tempo, prometheus, loki; three pipelines.
- [ ] T46. `deploy/observability/{tempo.yaml,loki-config.yaml,prometheus.yml}` — prometheus scrapes collector; `--enable-feature=exemplar-storage`.
- [ ] T47. `deploy/observability/grafana/provisioning/datasources/*.yaml` — Prometheus, Tempo, Loki; Tempo↔Loki `tracesToLogs`/`logsToTraces` by `trace_id`; Prometheus exemplars → Tempo.
- [ ] T48. `deploy/observability/grafana/dashboards/*.json` — RED, latency heatmap, throughput, error-rate, resource stats (goroutines/GC/pgx pool/valkey), apilog drops + breaker states; provisioned at boot.
- [ ] T49. `docker-compose.observability.yml` + Makefile `obs-up`/`obs-down`.
- [ ] T50. Benchmark matrix — `load.js` across `OTEL_ENABLED` × `APILOG_ENABLED` on/off; assert p95 delta < 5% (NFR-P2); `make load-matrix` appends delta summary to logbook.

### 5d. Lifecycle

- [ ] T51. `internal/health/health.go` — `GET /health` liveness envelope; `GET /ready` runs `SELECT 1` + Valkey `PING` in parallel → 200 all-pass or 503 with failure detail (R9, F-9).
- [ ] T52. Graceful shutdown in `main.go` — `signal.NotifyContext(SIGINT, SIGTERM)`; order `http.Server.Shutdown` → `apilog.Drain` → `telemetry` shutdown → `pool.Close` → Valkey close; log each step; exit 0 clean / 1 timeout (R13, NFR-R3).
- [ ] T53. Verify shutdown under load — SIGTERM mid-flight: in-flight complete, audit buffer flushed, no span/metric loss; integration test simulates the sequence.

### 5e. Cloud rung

- [ ] T54. Provision cloud (AWS, same region): API `c7g.xlarge`, Postgres `c6i.large`, k6 `c6i.2xlarge`; **$10 billing alarm before starting** (R24).
- [ ] T55. Deploy binary; configure env; `make load-smoke BASE_URL=<url>` to verify healthy.
- [ ] T56. Add PgBouncer (transaction mode, pool ~`2×DB_MAX_CONNS`); re-run `RPS=1000` (no regression), then `RPS=5000`, `RPS=10000` (NFR-S2).
- [ ] T57. Load balancer (ALB/nginx) + 2–3 replicas; `load.js RPS=10000`; confirm p95 < 100ms, < 1% errors; record (NFR-S1/S3).
- [ ] T58. Prometheus + Grafana up (reuse `docker-compose.observability.yml`); verify RED panels + k6 remote-write land. **Tear down all instances; record actual cost in logbook.**

**Exit (M5):** p95 < 100ms @ 10K rps in-cloud; telemetry off-path (< 5% matrix delta); rate-limit + audit live & non-blocking; SIGTERM drains clean; day cost < $5; instances terminated.

---

## Rung 6 — Distributed (100K rps, stretch) · _M6_

> Only after M5 validated and 10K data in hand.

- [ ] T59. Evaluate distributed-DB choice from rung-5 access patterns (Citus / CockroachDB / read-replica fan-out + CDN edge); write `docs/adr/0007-rung6-db-strategy.md`.
- [ ] T60. Implement the chosen DB strategy; validate with 20–50 replicas + LB.
- [ ] T61. Implement the real `queue` sink (Kafka/NATS) and switch `APILOG_SINK=queue` so Postgres audit does not saturate at this scale (R8).
- [ ] T62. **Hot-key protection** — L1 in-process cache / key-splitting if a single Valkey shard saturates on one key; reproduce the shard-hot-spot first, then prove the fix.
- [ ] T63. Distributed k6 to 100K rps; confirm p95 < 200ms; record in logbook.

**Exit (M6):** p95 < 200ms @ 100K rps; logbook complete; ADR 0007 written.

---

## Consumer-driven — Outbound resilience (no rung)

`breaker`/`retry`/`registry` + `httpclient` protect **outbound** calls only. Off the RPS ladder — an inbound read path with no upstream pays zero (NFR-P2). Preserved in `phase5-built`; wired back when a real upstream lands.

- [ ] T64. Re-introduce `internal/resilience/{retry,breaker,registry}` from `phase5-built`; unit tests: breaker opens after `BREAKER_FAIL_THRESHOLD`, retry backoff+jitter, 4xx does **not** trip the breaker (R12, NFR-R1/R2).
- [ ] T65. Re-introduce `internal/httpclient` + `internal/sanitize` from `phase5-built`; SSRF guard in `DialContext` on the resolved addr; test: private-IP URL rejected pre-dial; sensitive keys masked in logged bodies (R7, R14, NFR-SEC4/SEC5).
- [ ] T66. Wire a real SSRF-guarded, `Resilient`-wrapped outbound call behind the endpoint that needs it; integration test end-to-end (F-7).

---

## ADRs (draft as each rung lands its decision)

- [x] `0001-go-postgres-scaling-testbed.md` — stack.
- [ ] `0002-config-and-secrets.md`
- [ ] `0003-resilience-backend.md` — + earn-the-complexity sequencing (resilience consumer-driven, cache at rung 4).
- [ ] `0004-error-envelope.md`
- [ ] `0005-api-audit-log.md`
- [ ] `0006-observability.md`
- [ ] `0007-cache-hardening.md` — TTL jitter / singleflight / XFetch / bloom / hot-key, each earned by a reproduced failure.
