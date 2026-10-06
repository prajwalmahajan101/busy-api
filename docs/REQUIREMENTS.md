# busy-api — Requirements

Requirements for a scalable JSON API on Go + Gin, porting the reusable
infrastructure of the Python FastAPI core (`colending_partner/src/core`).
The build sequence lives in `docs/ROADMAP.md`; this file is the source of
truth for **what** the system must do and the constraints it must meet.

---

## 1. Overview & goals

- Provide a production-grade Go/Gin HTTP API foundation: config, structured logging, error handling, persistence, caching, resilience, async audit, and full observability.
- Scale from **1K RPS on a single instance** to **~100K RPS** via horizontal replicas behind a load balancer, without code changes — scale is an infra concern, the process stays stateless.
- Reuse proven patterns from the Python core, translated to Go idiom (middleware + context + wrapper funcs instead of decorators + ContextVars).

## 2. Scope

**In scope:** config/secrets loading, request-id propagation, response envelope, error hierarchy, HTTP middleware stack, Postgres persistence (pgx + sqlc), Valkey cache, resilience (retry / circuit breaker / throttle), async API audit log, outbound HTTP client with SSRF guard, health/readiness, graceful shutdown, and full telemetry (traces / metrics / logs → Grafana).

**Out of scope (deferred):** authentication/API-keys, field encryption (Fernet), S3/SES/PII utils, AWS Secrets Manager fetch (seam only), the queue audit sink real impl (Kafka/NATS + ClickHouse), CI.

## 3. Functional requirements

| ID | Requirement |
|---|---|
| F-1 | Expose versioned REST endpoints returning a single JSON envelope: `{success, message, data, errors[], request_id}`. |
| F-2 | Every response carries a request id (accept inbound `X-Request-ID` matching `^[A-Za-z0-9-]{1,128}$`, else mint UUIDv4) echoed as a header and in the body. |
| F-3 | CRUD over Postgres via sqlc-generated typed queries; list endpoints paginated (page/size → `total_pages`/`has_next`/`has_prev`), index-backed. |
| F-4 | Soft-delete (`is_active=false`) is the default delete; hard delete explicit only. |
| F-5 | Errors map to a typed hierarchy → correct HTTP status + error envelope (see §8). |
| F-6 | Hot reads may be served cache-aside from Valkey; cache is fail-open (Valkey down ⇒ serve from DB, never 5xx). |
| F-11 | The cache is **tiered and self-healing**: a bounded in-process L1 fronts Valkey (L1 → L2 Valkey → DB) so a Valkey outage does not flood the DB; a circuit breaker around Valkey stops dialing a down backend and auto-recovers. Hardened against avalanche (TTL jitter), stampede (`singleflight`/XFetch), penetration (bloom filter), and hot keys (key-splitting), each proven by a reproduced k6 failure. |
| F-7 | Outbound calls to upstreams go through a pooled client with per-call auth, error mapping, SSRF guard, and resilience wrapping. |
| F-8 | Every inbound request is audit-logged asynchronously (method, url, status, duration, sizes, error, request_id) without blocking the response. |
| F-9 | `/health` (liveness) and `/ready` (readiness: Postgres + Valkey) endpoints. |
| F-10 | Rate limiting available per-route (sliding window) returning `429` + `X-RateLimit-*` headers. |

## 4. Non-functional requirements

### 4.1 Performance
- **NFR-P1:** p95 < 200ms for user-facing endpoints (excluding downstream/DB pathological cases), per global backend budget.
- **NFR-P2:** Framework/middleware/telemetry overhead **sub-ms p50**, single-digit % throughput — kept off the synchronous path (see §7).
- **NFR-P3:** No synchronous DB write on the audit path; audit is queue + batch.
- **NFR-P4:** The only permitted per-request synchronous network add is the opt-in throttle Valkey round-trip.

### 4.2 Scalability
- **NFR-S1:** Process is fully stateless — all state in Postgres/Valkey — so N replicas scale linearly behind a load balancer.
- **NFR-S2:** Connection pools sized per instance (`pgxpool` ~`4×cores`); at high replica counts, pgbouncer fronts Postgres to stay under `max_connections`.
- **NFR-S3:** Path to 100K RPS is infra (replicas + LB + read replicas + pgbouncer + cache hit-rate), requiring no application rewrite.

### 4.3 Reliability & resilience
- **NFR-R1:** Outbound calls wrapped in circuit breaker (CLOSED/OPEN/HALF_OPEN) + retry with exponential backoff + jitter.
- **NFR-R2:** Circuit breaker trips only on **transient/timeout** errors (5xx, network, timeout); never on business 4xx or an upstream 4xx rejection (`ExternalServiceError`) — a 4xx means the upstream is healthy and rejected our request, so retrying/tripping would be wrong.
- **NFR-R3:** Graceful shutdown on SIGINT/SIGTERM: stop accepting → drain in-flight → flush audit buffer → flush telemetry → close pools.
- **NFR-R4:** Audit buffer is bounded; overflow drops records (counted), never blocks or OOMs.
- **NFR-R5:** The cache is self-healing: a circuit breaker around Valkey OPENs after repeated failures so a down/slow Valkey is **skipped** (no per-request dial tax) rather than dialed every call, and HALF_OPEN probes auto-recover when Valkey returns. A bounded in-process L1 tier absorbs reads during the outage so DB load stays flat; L1 is capped (`CACHE_L1_MAX`) and cannot OOM.

### 4.4 Security
- **NFR-SEC1:** Validate at boundaries; parameterized queries only (sqlc); no string-built SQL.
- **NFR-SEC2:** Reject request bodies over `MAX_BODY_BYTES` with 413.
- **NFR-SEC3:** Security headers on every response (HSTS in prod, X-Content-Type-Options, X-Frame-Options=DENY, Referrer-Policy, CSP, Permissions-Policy).
- **NFR-SEC4:** SSRF guard on every outbound URL (reject private/loopback/link-local/metadata IPs).
- **NFR-SEC5:** Log sanitization masks sensitive keys (`password|token|secret|key|authorization`) and caps sizes before emit.
- **NFR-SEC6:** Secrets from env / secret-manager, never in code; `.env` git-ignored.

### 4.5 Observability
- **NFR-O1:** Distributed traces via OpenTelemetry → Tempo; auto server span per request (otelgin).
- **NFR-O2:** RED metrics (rate/errors/duration) per endpoint via OTel metrics → Prometheus; plus app instruments (audit drops, breaker state, cache hit/miss, throttle rejects, pgx pool stats).
- **NFR-O3:** Structured JSON logs → Loki, correlated to traces by `trace_id`/`span_id`; `request_id` on every line.
- **NFR-O4:** Grafana with provisioned datasources + dashboards; trace↔logs↔metrics correlation (exemplars metric→trace, trace→logs).
- **NFR-O5:** `OTEL_ENABLED=false` fully disables exporters (stdout logs only) for local/benchmarks.

### 4.6 Maintainability
- **NFR-M1:** Layering controller (gin handler) → service → repository (sqlc); business logic in service layer, handlers thin.
- **NFR-M2:** Each subsystem behind an interface where a backend is swappable (cache, breaker, throttle, audit sink) — memory/local default, distributed impl behind config.
- **NFR-M3:** One decision per ADR in `docs/adr/`.
- **NFR-M4:** Each phase ships an integration test against real Postgres/Valkey.

## 5. Technology stack

| Layer | Choice |
|---|---|
| Language / runtime | Go 1.26 |
| HTTP | Gin |
| Datastore | PostgreSQL (existing instance, via `DATABASE_URL`) |
| SQL layer | pgx/v5 + sqlc (typed queries) |
| Migrations | goose |
| Cache / resilience backend | Valkey (Redis-compatible), go-redis client |
| Config | caarlos0/env + godotenv; cloud secrets via env injection (+ pre-load seam) |
| Telemetry | OpenTelemetry → OTel Collector → Tempo (traces) + Prometheus (metrics) + Loki (logs) → Grafana |
| Testing | Go testing + testify, integration against real PG/Valkey |
| Load testing | k6 (scripts in `loadtest/`, thresholds as pass/fail, results remote-written to Prometheus → Grafana) |
| Dev tooling | Makefile, golangci-lint, air |

## 6. Pinned dependencies

**Runtime modules (`go get`):**

| Module | Version |
|---|---|
| `github.com/gin-gonic/gin` | `v1.12.0` |
| `github.com/jackc/pgx/v5` | `v5.11.0` |
| `github.com/redis/go-redis/v9` | `v9.22.0` |
| `github.com/caarlos0/env/v11` | `v11.4.1` |
| `github.com/joho/godotenv` | `v1.5.1` |
| `github.com/google/uuid` | `v1.6.0` |
| `gopkg.in/natefinch/lumberjack.v2` | `v2.2.1` |
| `github.com/stretchr/testify` | `v1.12.1` |
| `go.opentelemetry.io/otel` | `v1.46.0` |
| `go.opentelemetry.io/otel/sdk` | `v1.46.0` |
| `go.opentelemetry.io/otel/sdk/metric` | `v1.46.0` |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` | `v1.46.0` |
| `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc` | `v1.46.0` |
| `go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin` | `v0.71.0` |
| `go.opentelemetry.io/contrib/bridges/otelslog` | `v0.20.1` |

**Tools (`go install`):** sqlc `v1.31.1`, goose `v3.28.0`, air `v1.67.4`, golangci-lint `v2.13.2`.

**Load testing:** k6 (binary via `brew`/`apt`/release — not a Go dependency); scripts in `loadtest/`.

**Infra images (docker-compose):** `valkey/valkey:8-alpine`, `otel/opentelemetry-collector-contrib:0.116.1`, `grafana/tempo:2.6.1`, `grafana/loki:3.3.2`, `prom/prometheus:v3.1.0`, `grafana/grafana:11.4.0`.

## 7. Overhead budget (per request, ranked)

| Component | Hot-path cost | Rule |
|---|---|---|
| Throttle (rate-limit) | 1 Valkey RTT ~0.2–1ms | per-route only; skip hot reads |
| Tracing | span alloc + async export | prod sample ≤ 10% |
| Metrics | in-memory, async 10s export | ~free |
| Logs → OTLP | batched async | keep `INFO` |
| Audit | one non-blocking channel send | drop-on-full |
| Middleware | header ops + one regex | µs |
| Resilience | not per-request (outbound only) | zero on no-upstream requests |

Target: sub-ms p50 overhead. Postgres is the ceiling before this shows.

## 8. Error model

`AppError { Code, Message, HTTPStatus, Details, RequestID }` → gin handler → error envelope.

| Type | Status | Trips breaker? |
|---|---|---|
| `ValidationError` | 422 | no |
| `NotFoundError` | 404 | no |
| `RateLimitError` | 429 | no |
| `InfrastructureError` | 500 | no |
| `ServiceUnavailableError` | 503 | no (breaker source) |
| `ExternalServiceError` | 502 | no |
| `TransientError` | 502 | yes |
| `ExternalTimeoutError` | 502 | yes |

Unknown errors → 500 generic envelope (no internal detail leaked).

## 9. Configuration (environment variables)

| Var | Required | Default | Notes |
|---|---|---|---|
| `PORT` | no | `8000` | listen port |
| `ENV` | no | `local` | local/staging/prod — gates HSTS, docs, secret source |
| `DATABASE_URL` | **yes** | — | `postgres://…?sslmode=disable` |
| `DB_MAX_CONNS` | no | `4×cores` | pgxpool max |
| `DB_CONN_TIMEOUT_MS` | no | `5000` | pgxpool connect timeout |
| `DB_QUERY_TIMEOUT_MS` | no | `2000` | per-call query deadline (`context.WithTimeout` on every DB call) |
| `VALKEY_URL` | no | `redis://localhost:6379/0` | empty ⇒ in-memory fallback |
| `CACHE_ITEM_TTL_S` | no | `300` | cache-aside TTL for single-item reads (seconds) |
| `CACHE_L1_ENABLED` | no | `true` | in-process L1 tier in front of Valkey (L1→L2→DB) |
| `CACHE_L1_MAX` | no | `10000` | bounded L1 entries (LRU) — cannot OOM on Valkey outage |
| `CACHE_L1_TTL_S` | no | `30` | short L1 TTL |
| `CACHE_BREAKER_FAIL_THRESHOLD` | no | `5` | Valkey failures before the cache breaker OPENs |
| `CACHE_BREAKER_RECOVERY_S` | no | `10` | cache breaker OPEN→HALF_OPEN probe interval |
| `CACHE_TTL_JITTER_PCT` | no | `10` | ±% jitter on every cache Set TTL (avalanche fix) |
| `CACHE_HOTKEY_SPLITS` | no | `1` | replica sub-keys per hot key (`>1` enables splitting) |
| `LOG_LEVEL` | no | `INFO` | DEBUG/INFO/WARNING/ERROR |
| `LOG_JSON` | no | `true` | text if false |
| `LOG_FILE` | no | `` | path ⇒ rotate+gzip |
| `LOG_MAX_MB` | no | `10` | rotate size |
| `LOG_BACKUP_COUNT` | no | `5` | retained backups |
| `MAX_BODY_BYTES` | no | `1048576` | body-limit (413) |
| `CORS_ORIGINS` | no | `` | allowlist; empty ⇒ off |
| `RETRY_MAX` | no | `3` | resilience default |
| `RETRY_BASE_MS` | no | `200` | backoff base |
| `BREAKER_FAIL_THRESHOLD` | no | `5` | trips breaker |
| `BREAKER_RECOVERY_S` | no | `30` | OPEN→HALF_OPEN |
| `APILOG_ENABLED` | no | `true` | audit master switch |
| `APILOG_SINK` | no | `postgres` | `postgres` \| `queue` |
| `APILOG_BUFFER` | no | `10000` | channel cap; full ⇒ drop+count |
| `APILOG_WORKERS` | no | `2` | drain goroutines |
| `APILOG_BATCH_SIZE` | no | `500` | flush at N records |
| `APILOG_FLUSH_MS` | no | `1000` | max flush interval |
| `APILOG_SAMPLE_2XX` | no | `0.1` | 2xx sample fraction; non-2xx always |
| `APILOG_CAPTURE_BODY` | no | `false` | capture bodies (capped, sanitized) |
| `APILOG_MAX_BODY_BYTES` | no | `4096` | body capture cap |
| `APILOG_TTL_DAYS` | no | `30` | audit retention |
| `OTEL_ENABLED` | no | `true` | false ⇒ no exporters, stdout logs |
| `OTEL_SERVICE_NAME` | no | `busy-api` | resource `service.name` |
| `OTEL_SERVICE_VERSION` | no | `dev` | resource `service.version` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | no | `http://localhost:4317` | OTLP gRPC to Collector |
| `OTEL_EXPORTER_OTLP_INSECURE` | no | `true` | plaintext OTLP local |
| `OTEL_TRACES_SAMPLER` | no | `parentbased_traceidratio` | sampler |
| `OTEL_TRACES_SAMPLER_ARG` | no | `0.1` | sample fraction (1.0 dev) |
| `OTEL_METRIC_EXPORT_INTERVAL_MS` | no | `10000` | metric push period |

## 10. Data & persistence

- Base model columns on every table: `id bigserial pk`, `created_at timestamptz`, `updated_at timestamptz`, `is_active bool` (indexed, soft-delete), `notes jsonb`.
- Audit table `api_log` daily-partitioned; indexes on `request_id`, `timestamp`, `status`; retention by dropping old partitions.
- Migrations versioned in `migrations/` (goose); schema is the sqlc source of truth.
- Each service owns its schema; no cross-service DB reads.

## 11. Acceptance criteria (per requirement group)

- **Functional:** every `F-*` has an endpoint + integration test proving the envelope, status, pagination, soft-delete, cache fail-open, and async audit behaviors.
- **Performance:** k6 `load` profile enforces `http_req_duration p(95)<200ms` + `http_req_failed rate<0.01` as pass/fail thresholds; benchmark matrix runs each profile with audit + telemetry ON vs OFF to prove p95 unchanged (off-hot-path); throttle overhead measured and ≤ 1ms. k6 results remote-written to Prometheus, viewed in Grafana next to server RED/traces.
- **Scale ladder:** validate in 10× rungs — **1 → 10 → 100 → 1K → 10K → 100K RPS** — each rung must hold the thresholds before climbing; insert half-decade rungs (500, 2K, 5K, 50K) to bisect a break point. Expected: 1→1K on one stateless instance (tuning only); 10K needs pgbouncer + cache + read replicas starting to matter; 100K is horizontal (20–50 replicas + LB), no app rewrite. Record p95/error/resource per rung.
- **Resilience:** injected upstream failures open the breaker after threshold; SIGTERM drains in-flight + flushes audit + telemetry within the drain timeout.
- **Security:** oversized body → 413; private-IP outbound URL rejected; sensitive keys masked in logs.
- **Observability:** a request produces a span in Tempo, correlated logs in Loki (by `trace_id`), and RED panels in Grafana from Prometheus; a metric exemplar jumps to its trace.

## 12. Deferred / future

- Auth: API-key gen + HMAC-SHA256 lookup + encrypted copy (add when endpoints need protection).
- Field encryption (Fernet / AES-256-GCM), S3/SES/PII utils — per feature need.
- AWS Secrets Manager fetch — implement the Phase-0 seam on cloud deploy.
- `queue` audit sink (Kafka/NATS → ClickHouse) — build when Postgres audit saturates on the 100K path.
- CI (GitHub Actions) — added: `.github/workflows/ci.yml` (build · vet · test · golangci-lint). Integration job (real Postgres + Valkey) follows the `integration` build tag in Phase 3/4.
