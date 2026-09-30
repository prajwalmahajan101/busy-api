# busy-api — Core Port Roadmap

Porting the shared infrastructure from the Python FastAPI project
(`colending_partner/src/core`, ~13k lines) into this Go/Gin service.
Task-level, phase-sequenced. Each phase ships behind a real endpoint and
ends with a runnable exit check.

Scaling target: 1K RPS on one instance, ~100K RPS by running N stateless
replicas behind a load balancer. Throughput is infra (replicas + LB +
Postgres read replicas + pgbouncer + cache hit-rate), not code — every
phase below stays stateless so replicas are the only lever needed. The
one component that could fight this — the API audit log — is designed
async and off the hot path (Phase 7).

## Decisions locked

| Topic | Decision |
|---|---|
| HTTP framework | Gin (`v1.12.0`) — already in use. |
| Datastore | **PostgreSQL** — instance already running; connect via `DATABASE_URL`. No Postgres container. |
| SQL layer | **pgx/v5 + sqlc** — pooled pgx, sqlc-generated typed queries from `.sql` files. |
| Migrations | **goose** (`pressly/goose/v3`) — SQL migrations in `migrations/`, versioned up/down. |
| Cache / resilience backend | **Valkey** via `docker-compose.yml`, client **`redis/go-redis/v9`** (Valkey speaks RESP). |
| Config | **`caarlos0/env/v11`** struct tags + **`joho/godotenv`** for local `.env`. Cloud injects secrets as env; optional AWS Secrets Manager pre-load hook (seam only). |
| Testing | Go `testing` + **`stretchr/testify/require`**, integration tests against real Postgres + Valkey. |
| Tooling | **Makefile**, **golangci-lint** (`v2`), **air** hot-reload, **k6** load testing (scripts in `loadtest/`, results remote-written to Prometheus → Grafana). No CI yet (repo is not git-initialized). |
| API audit log | **Included, async & non-blocking** — bounded queue + batch writer, sampling, drop-on-overflow, pluggable sink (Postgres default, queue sink for 100K). Never touches the request hot path. See Phase 7. |
| Observability | **OpenTelemetry** SDK emits traces + metrics + logs over **OTLP** to an **OTel Collector**, which fans out: traces → **Tempo**, metrics → **Prometheus**, logs → **Loki**; visualized in **Grafana** (provisioned datasources + RED dashboards, trace↔logs↔metrics correlation via `trace_id`/exemplars). Full local stack via `docker-compose.observability.yml`. See Phase 8. |
| Scope | Essentials + resilience + async audit + full observability. |
| Auth | **Deferred** — no `auth/` port now. |

## Dependencies (pinned, verified against proxy)

**Runtime (`go get`):**

| Module | Version | Used by |
|---|---|---|
| `github.com/gin-gonic/gin` | `v1.12.0` | HTTP |
| `github.com/jackc/pgx/v5` | `v5.11.0` | Postgres pool + queries + `CopyFrom` batch |
| `github.com/redis/go-redis/v9` | `v9.22.0` | Valkey client |
| `github.com/caarlos0/env/v11` | `v11.4.1` | env → Config struct |
| `github.com/joho/godotenv` | `v1.5.1` | load local `.env` |
| `github.com/google/uuid` | `v1.6.0` | request-id (already added) |
| `gopkg.in/natefinch/lumberjack.v2` | `v2.2.1` | log rotation (already added) |
| `github.com/stretchr/testify` | `v1.12.1` | test asserts |
| `go.opentelemetry.io/otel` | `v1.46.0` | tracing/metrics API |
| `go.opentelemetry.io/otel/sdk` | `v1.46.0` | trace SDK + resource |
| `go.opentelemetry.io/otel/sdk/metric` | `v1.46.0` | metric SDK |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc` | `v1.46.0` | OTLP trace exporter |
| `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc` | `v1.46.0` | OTLP metric exporter |
| `go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin` | `v0.71.0` | auto HTTP spans + RED metrics |
| `go.opentelemetry.io/contrib/bridges/otelslog` | `v0.20.1` | slog → OTLP logs (Loki) |

**Tools (`go install`, not imported):**

| Tool | Version | Install |
|---|---|---|
| sqlc | `v1.31.1` | `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1` |
| goose | `v3.28.0` | `go install github.com/pressly/goose/v3/cmd/goose@v3.28.0` |
| air | `v1.67.4` | `go install github.com/air-verse/air@v1.67.4` |
| golangci-lint | `v2.13.2` | `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2` |

## Environment variables

| Var | Required | Default | Notes |
|---|---|---|---|
| `PORT` | no | `8000` | HTTP listen port |
| `ENV` | no | `local` | `local`/`staging`/`prod` — gates docs, HSTS, secret source |
| `DATABASE_URL` | **yes** | — | `postgres://user:pass@host:5432/db?sslmode=disable` |
| `DB_MAX_CONNS` | no | `4×cores` | pgxpool max conns |
| `DB_CONN_TIMEOUT_MS` | no | `5000` | pgxpool connect timeout |
| `DB_QUERY_TIMEOUT_MS` | no | `2000` | per-call query deadline (`context.WithTimeout` on every DB call) |
| `VALKEY_URL` | no | `redis://localhost:6379/0` | resilience backend; empty ⇒ in-memory fallback |
| `LOG_LEVEL` | no | `INFO` | DEBUG/INFO/WARNING/ERROR |
| `LOG_JSON` | no | `true` | false ⇒ text handler |
| `LOG_FILE` | no | `` (stdout only) | path ⇒ rotate+gzip |
| `LOG_MAX_MB` | no | `10` | rotate size |
| `LOG_BACKUP_COUNT` | no | `5` | retained backups |
| `MAX_BODY_BYTES` | no | `1048576` | body-limit middleware (413) |
| `CORS_ORIGINS` | no | `` | comma-separated allowlist; empty ⇒ CORS off |
| `RETRY_MAX` | no | `3` | resilience default |
| `RETRY_BASE_MS` | no | `200` | backoff base |
| `BREAKER_FAIL_THRESHOLD` | no | `5` | trips breaker |
| `BREAKER_RECOVERY_S` | no | `30` | OPEN→HALF_OPEN |
| `APILOG_ENABLED` | no | `true` | master switch for the audit log |
| `APILOG_SINK` | no | `postgres` | `postgres` (batch INSERT) or `queue` (publish to Kafka/NATS for 100K) |
| `APILOG_BUFFER` | no | `10000` | bounded channel capacity; full ⇒ drop + count, never block |
| `APILOG_WORKERS` | no | `2` | drain goroutines |
| `APILOG_BATCH_SIZE` | no | `500` | flush when batch reaches this |
| `APILOG_FLUSH_MS` | no | `1000` | flush at least this often |
| `APILOG_SAMPLE_2XX` | no | `0.1` | fraction of 2xx sampled; non-2xx always logged |
| `APILOG_CAPTURE_BODY` | no | `false` | capture req/resp bodies (size-capped, sanitized) |
| `APILOG_MAX_BODY_BYTES` | no | `4096` | per-body capture cap |
| `APILOG_TTL_DAYS` | no | `30` | audit retention |
| `OTEL_ENABLED` | no | `true` | false ⇒ no exporters, logs to stdout only |
| `OTEL_SERVICE_NAME` | no | `busy-api` | resource `service.name` |
| `OTEL_SERVICE_VERSION` | no | `dev` | resource `service.version` (set from build info) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | no | `http://localhost:4317` | OTLP gRPC to the Collector |
| `OTEL_EXPORTER_OTLP_INSECURE` | no | `true` | plaintext OTLP for local/in-cluster |
| `OTEL_TRACES_SAMPLER` | no | `parentbased_traceidratio` | sampler |
| `OTEL_TRACES_SAMPLER_ARG` | no | `0.1` | sample fraction (1.0 in dev) |
| `OTEL_METRIC_EXPORT_INTERVAL_MS` | no | `10000` | metric push period |

`internal/config` fails fast if a `required` var is missing. OTel reads
the standard `OTEL_*` env directly; the table lists the ones we set defaults for.

## Error family → HTTP status

Mirrors the Python `BaseCustomError` hierarchy. Every `AppError` carries
`code`, `httpStatus`, `message`, `details`, `requestID`; the gin handler
maps it into the error envelope.

| Go type | Status | Trips breaker? | Python origin |
|---|---|---|---|
| `ValidationError` | 422 | no | `validation.ValidationError` |
| `NotFoundError` | 404 | no | `repository.EntityNotFoundError` |
| `RateLimitError` | 429 | no | `rate_limit.RateLimitError` |
| `InfrastructureError` | 500 | no | `infrastructure.InfrastructureError` |
| `ServiceUnavailableError` | 503 | no (breaker source) | `infrastructure.ServiceUnavailableError` |
| `ExternalServiceError` | 502 | no | `infrastructure.ExternalServiceError` |
| `TransientError` | 502 | **yes** | `infrastructure.TransientError` |
| `ExternalTimeoutError` | 502 | **yes** | `infrastructure.ExternalTimeoutError` |

Business errors (422/404/429) never trip the breaker; nor does `ExternalServiceError`
(an upstream 4xx rejection — the upstream is healthy). Only `TransientError` and
`ExternalTimeoutError` (5xx / network / timeout) trip it.

## Idiom translation (Python → Go)

| Python pattern | Go equivalent |
|---|---|
| `ContextVar` request-id | `context.Context` threaded through calls (wired in `internal/logging`; moving to `internal/reqcontext`). |
| Decorators (`@resilient`, `@retry`, `@log_*`) | Middleware at the HTTP edge; higher-order wrapper funcs for outbound calls. |
| `@log_inbound_request` fire-and-forget | Edge middleware builds a `Record` and non-blocking `Enqueue`s to a bounded channel; a worker pool batch-writes. No DB in the request path. |
| SQLAlchemy async ORM | pgx/v5 pool + sqlc-generated queries; generic repo helpers over them. |
| Pydantic `BaseSettings` (Secrets>env>.env) | `godotenv` (local) → `caarlos0/env` parse; optional Secrets Manager pre-load sets env first. |
| Fernet field encryption | deferred with auth; `fernet-go` if cross-language compat needed, else AES-256-GCM (stdlib). |
| `provider`/`registry` singletons | package constructors returning interfaces; Valkey primary + in-memory fallback. |

## Target layout

```text
cmd/server/main.go          # wiring only
internal/
  config/        # caarlos0/env struct + godotenv + secrets seam
  reqcontext/    # request-id context helpers
  response/      # envelope + Success/Error/Paginated
  errs/          # AppError hierarchy + gin handler + status registry
  middleware/    # requestid, logging, recovery, bodylimit, secheaders, cors, ratelimit, apilog
  logging/       # DONE
  db/            # pgxpool, Atomic() tx helper
  store/         # sqlc output (db/) + generic repo/service helpers
  resilience/
    retry/       # backoff + jitter
    breaker/     # circuit breaker: memory + valkey, provider
    cache/       # memory + valkey, provider
    throttle/    # sliding window, scopes, provider
    registry/    # per-service config + Resilient() composition
  apilog/        # Record, Collector (bounded queue), worker pool, Sink (postgres|queue)
  telemetry/     # OTel init: TracerProvider, MeterProvider, log bridge, RED metrics, shutdown
  valkey/        # go-redis client cache by alias
  httpclient/    # pooled net/http client + SSRF guard + Resilient()
  health/        # health + readiness routers
migrations/      # goose SQL migrations
deploy/observability/
  otel-collector-config.yaml   # OTLP in → Tempo/Prometheus/Loki out
  prometheus.yml
  tempo.yaml
  loki-config.yaml
  grafana/provisioning/datasources/*.yaml   # Prometheus, Tempo, Loki (with trace↔logs links)
  grafana/provisioning/dashboards/*.yaml
  grafana/dashboards/*.json                 # RED, latency heatmap, throughput, resource stats
loadtest/
  smoke.js        # 1 VU, sanity
  load.js         # ramp to target RPS, p95 threshold
  stress.js       # push past target until break
  spike.js        # sudden burst
  soak.js         # sustained, memory/leak watch
  lib/            # shared checks, thresholds, helpers
sqlc.yaml        # sqlc config
docs/adr/        # one ADR per phase decision
Makefile
.air.toml
.golangci.yml
.env.example
docker-compose.yml                # valkey only (app deps)
docker-compose.observability.yml  # otel-collector, prometheus, tempo, loki, grafana
```

## docker-compose.yml (valkey only)

```yaml
services:
  valkey:
    image: valkey/valkey:8-alpine
    ports:
      - "6379:6379"
    volumes:
      - valkey-data:/data
    command: ["valkey-server", "--appendonly", "yes"]
    healthcheck:
      test: ["CMD", "valkey-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5
volumes:
  valkey-data:
```

## docker-compose.observability.yml (LGTM stack)

```yaml
services:
  otel-collector:
    image: otel/opentelemetry-collector-contrib:0.116.1
    command: ["--config=/etc/otelcol/config.yaml"]
    volumes:
      - ./deploy/observability/otel-collector-config.yaml:/etc/otelcol/config.yaml:ro
    ports: ["4317:4317", "4318:4318"]   # OTLP gRPC / HTTP
    depends_on: [tempo, loki, prometheus]

  tempo:
    image: grafana/tempo:2.6.1
    command: ["-config.file=/etc/tempo.yaml"]
    volumes:
      - ./deploy/observability/tempo.yaml:/etc/tempo.yaml:ro

  loki:
    image: grafana/loki:3.3.2
    command: ["-config.file=/etc/loki/config.yaml"]
    volumes:
      - ./deploy/observability/loki-config.yaml:/etc/loki/config.yaml:ro

  prometheus:
    image: prom/prometheus:v3.1.0
    command: ["--config.file=/etc/prometheus/prometheus.yml", "--enable-feature=exemplar-storage"]
    volumes:
      - ./deploy/observability/prometheus.yml:/etc/prometheus/prometheus.yml:ro

  grafana:
    image: grafana/grafana:11.4.0
    ports: ["3000:3000"]
    environment:
      GF_AUTH_ANONYMOUS_ENABLED: "true"
      GF_AUTH_ANONYMOUS_ORG_ROLE: "Admin"
    volumes:
      - ./deploy/observability/grafana/provisioning:/etc/grafana/provisioning:ro
      - ./deploy/observability/grafana/dashboards:/var/lib/grafana/dashboards:ro
    depends_on: [prometheus, tempo, loki]
```

App exports OTLP to `otel-collector:4317`. Collector routes traces→Tempo,
metrics→Prometheus (via `prometheus` exporter scraped, or remote-write),
logs→Loki. Grafana at `localhost:3000` with all three datasources
pre-provisioned and correlated by `trace_id`.

## Performance & overhead budget

Everything above is designed to stay **off the synchronous request path**.
Per-request overhead, ranked:

| Component | Hot-path cost | Rule to stay fast |
|---|---|---|
| Throttle (rate-limit) | **1 Valkey RTT ~0.2–1ms** — the only synchronous network add | Enable per-route, not global; skip hot reads; in-memory for single instance. |
| Tracing | span alloc + async batch export | Prod sample ≤ 10% (`OTEL_TRACES_SAMPLER_ARG=0.1`); 1.0 only in dev. |
| Metrics | in-memory counter/histogram, async 10s export | Sub-µs; effectively free. |
| Logs → OTLP | batched, async via Collector | Already logging per request; keep `INFO`. |
| apilog audit | one channel `Enqueue`, non-blocking | Drop-on-full; no DB in path. |
| Middleware (reqid/sec-headers/cors/body-limit) | header ops + one regex | µs each. |
| Resilience (retry/breaker) | **not per-request** — wraps outbound only | Zero cost on requests with no upstream. |

Budget: sane config ⇒ **sub-ms p50 overhead**, single-digit % throughput.
Async exporters + sampling keep tracing/metrics/logs/audit off the
critical path; only opt-in throttle adds synchronous latency. `OTEL_ENABLED=false`
for raw benchmarks. Postgres remains the throughput ceiling long before
this overhead is visible.

## Load testing (k6)

k6 proves the budget above and validates the scaling model. Scripts in
`loadtest/`, thresholds encoded as pass/fail so runs are CI-gateable later.

- **Profiles:** `smoke` (sanity), `load` (ramp to target RPS, assert p95),
  `stress` (find the break point), `spike` (burst), `soak` (sustained,
  watch memory/leaks + audit drops).
- **Thresholds** (in-script, fail the run if breached): `http_req_duration p(95)<200ms`,
  `http_req_failed rate<0.01`, plus per-endpoint checks.
- **Results → Grafana:** run with `--out experimental-prometheus-rw` so k6
  metrics land in the same Prometheus and render on a k6 Grafana dashboard —
  load-gen numbers next to server RED/traces for one correlated view.
- **Benchmark matrix:** run each profile with `OTEL_ENABLED` and audit
  on/off to quantify overhead (proves NFR-P2/P3 — p95 unchanged on vs off).
- **Makefile:** `make load-smoke`, `make load`, `make load-stress`; target
  URL via `BASE_URL` env, VUs/duration/RPS parameterized per script.

### RPS ladder (climb one rung at a time)

Use a k6 `ramping-arrival-rate` scenario, `RPS` parameterized. Each rung
must hold thresholds (`p(95)<200ms`, `error<1%`) before climbing. Where it
breaks tells you the next bottleneck — fix, then climb.

| Rung | Setup | Proves | Fix if it fails here |
|---|---|---|---|
| 1 RPS | 1 instance, laptop | correctness, smoke | logic bug, not perf |
| 10 RPS | 1 instance | steady state, no leaks/goroutine growth | connection/leak handling |
| 100 RPS | 1 instance | pools warm, p95 budget met, cache working | index a slow query, size pgxpool |
| 1K RPS | 1 instance (tuned) | single-box ceiling | `GOMAXPROCS`, pgxpool ~`4×cores`, indexes, cache hot reads |
| 10K RPS | 1 tuned box or 2–3 replicas | DB contention appears | pgbouncer, raise cache hit-rate, read replicas start to matter |
| 100K RPS | 20–50 replicas + LB | horizontal scale-out | read replicas, pgbouncer, cache hit-rate, audit `queue` sink, LB tuning |

Rule: **1→1K needs no infra change** (single stateless box). 10K is where
Postgres contention starts — tuning + pgbouncer. 100K is pure horizontal:
replicas + LB + read replicas + cache, zero app rewrite. Record the p95 /
error / resource numbers at each rung in the k6 Grafana dashboard.

**Middle rungs when a jump breaks:** if a 10× jump fails (e.g. green at 1K,
red at 10K), bisect with a half-decade rung — 500, 2K, 5K, 50K — to pin the
exact break point before tuning. Same script, just change `RPS`. Don't
tune blind; find the cliff, fix that bottleneck, then resume the 10× climb.

Install: k6 binary (`brew`/`apt`/release). Not a Go dependency.

### Cache hardening sub-ladder (rung 4→6, symptom-gated)

Once cache-aside is on the hot read (rung 4), each hardening technique is
**earned by first reproducing its failure in k6, then proving the fix**
(before/after in the logbook). Never adopt them preemptively — an unproven
hardening is just complexity.

| Technique | Failure it fixes | Reproduce first | Rung |
|---|---|---|---|
| TTL jitter | avalanche — many keys expire at once ⇒ DB spike | warm N keys, identical TTL, watch synchronized-expiry spike | 4 |
| `singleflight` | stampede — hot key expires, M misses ⇒ M duplicate DB reads | hammer one key, expire mid-load, count dup queries | 4 |
| probabilistic early expiration (XFetch) | stampede tail — recompute before, not on, expiry | if p99 still spikes at the TTL boundary after `singleflight` | 4–5 |
| bloom filter | penetration — lookups for non-existent keys skip cache ⇒ DB | flood random non-existent ids, watch ~100% miss → DB | 5 |
| hot-key protection | one key saturates a single Valkey shard | rung 5–6 multi-node: one shard CPU-bound on one key | 5–6 |

Order within rung 4: TTL jitter (free) → `singleflight` (hard stampede floor)
→ XFetch (smooths the tail).

### What is *not* on the RPS ladder

**Outbound resilience** — `retry` / `breaker` / `registry` and the
`httpclient` (pooled transport, per-call auth, error mapping, SSRF guard) —
protects *outbound* calls only. An inbound read path with no upstream pays
**zero** for it (see overhead budget: "Resilience — not per-request"). It is
**consumer-driven**: wired back when a real upstream lands, not at any RPS
rung. Rate-limit **throttle** is protective, not a throughput lever; it enters
at rung 5 as edge hardening once a public LB exposes an abuse surface.

---

## Phase 0 — Infra & scaffolding  · size: S

**Tasks**
- [ ] `docker-compose.yml` (valkey) — content above; `docker compose up -d valkey`.
- [ ] `.env.example` with every var from the table; copy to `.env` locally.
- [ ] `internal/config/config.go` — `Config` struct with `env` tags; `Load() (*Config, error)` runs `godotenv.Load()` (ignore missing in prod) then `env.Parse`.
- [ ] Secrets seam: `loadCloudSecrets(ctx) error` no-op stub that later fetches Secrets Manager and `os.Setenv`s before `env.Parse`.
- [ ] `Makefile` targets: `run build test lint migrate-up migrate-down sqlc dev compose-up`.
- [ ] `.air.toml`, `.golangci.yml` committed.
- [x] `git init` + `.gitignore` (`.env`, `bin/`, `tmp/`, `*.log`, `logs/`) — repo initialized.
- [x] `go mod tidy`. Note: `mongo-driver/v2` is a required indirect dep of `gin v1.12.0` (`gin/binding` imports `bson`) — not removable.

**Key signatures**
```go
type Config struct {
    Port        string `env:"PORT" envDefault:"8000"`
    Env         string `env:"ENV" envDefault:"local"`
    DatabaseURL string `env:"DATABASE_URL,required"`
    ValkeyURL   string `env:"VALKEY_URL" envDefault:"redis://localhost:6379/0"`
    // ... rest of table
}
func Load() (*Config, error)
```

**Exit:** `docker compose up -d valkey` healthy; `make run` boots, logs loaded config with secrets masked, fails fast if `DATABASE_URL` unset.

## Phase 1 — Foundations  · size: M

No external deps — everything downstream imports these.

**Tasks**
- [ ] `internal/reqcontext` — move `WithRequestID`/`RequestIDFromContext` out of `logging` so `response` + `errs` use them without importing logging.
- [ ] `internal/response` — envelope + writers.
- [ ] `internal/errs` — `AppError` + typed constructors for every row in the error table + status registry + `Handler` gin middleware that converts `AppError` (and unknown errors → 500) into the error envelope.

**Key signatures**
```go
// response
type Envelope struct {
    Success   bool        `json:"success"`
    Message   string      `json:"message"`
    Data      any         `json:"data,omitempty"`
    Errors    []ErrDetail `json:"errors,omitempty"`
    RequestID string      `json:"request_id,omitempty"`
}
func Success(c *gin.Context, status int, msg string, data any)
func Error(c *gin.Context, err error) // resolves *errs.AppError → status+envelope
func Paginated(c *gin.Context, items any, page, size, total int)

// errs
type AppError struct {
    Code, Message string
    HTTPStatus    int
    Details       map[string]any
    RequestID     string
}
func (e *AppError) Error() string
func NewValidation(msg string, details map[string]any) *AppError
func NewNotFound(msg string) *AppError
func NewExternal(msg string) *AppError      // trips breaker
func NewTransient(msg string) *AppError     // trips breaker
func NewServiceUnavailable(service string) *AppError
func TripsBreaker(err error) bool
```

**Exit:** `/ping` returns success envelope; a failing route returns error envelope with correct status + `request_id`. testify test asserts both bodies.

## Phase 2 — HTTP edge (middleware stack)  · size: M

Install outermost→innermost (mirror Python `install_core_middleware`).

**Tasks (order = install order)**
- [ ] `middleware.Recovery()` → 500 error envelope (replaces `gin.Recovery` default body).
- [ ] `middleware.BodyLimit(maxBytes)` → 413 when `Content-Length` exceeds cap, AND wrap `c.Request.Body` in `http.MaxBytesReader` so the cap holds when `Content-Length` is absent/chunked/understated.
- [ ] `middleware.CORS(origins)` → allowlist; skip if `CORS_ORIGINS` empty.
- [ ] `middleware.SecurityHeaders(env)` → HSTS (prod only), X-Content-Type-Options, X-Frame-Options=DENY, Referrer-Policy, CSP, Permissions-Policy.
- [ ] `middleware.RequestID()` → accept inbound `X-Request-ID` matching `^[A-Za-z0-9-]{1,128}$`, else mint UUID; bind `reqcontext` + echo header.
- [ ] `middleware.RequestLogging(logger)` → extract current `requestLogger` from `main.go`; structured access log.
- [ ] `middleware.RateLimitHeaders()` → surface `X-RateLimit-*` (values populated in Phase 4).
- [ ] `middleware.APILog(collector)` → wired in Phase 7 (edge capture point reserved here).
- [ ] `main.go` reduced to: load config → setup logging → build router → install stack → register routes → graceful serve.

**Exit:** `main.go` is wiring only; every response carries `X-Request-ID` + security headers; oversized body → 413 envelope. testify covers 413 + header presence.

## Phase 3 — Persistence (Postgres, pgx + sqlc)  · size: L

**Tasks**
- [ ] `sqlc.yaml` — engine postgres, queries dir `internal/store/queries`, schema dir `migrations`, output `internal/store/db`, `emit_pointers_for_null_types`.
- [ ] `migrations/0001_base.sql` (goose) — first table with base columns: `id bigserial pk`, `created_at timestamptz default now()`, `updated_at timestamptz default now()`, `is_active bool default true` (indexed), `notes jsonb`.
- [ ] `internal/db` — `NewPool(ctx, cfg) (*pgxpool.Pool, error)` cached by DSN; `Atomic(ctx, pool, fn func(pgx.Tx) error) error` (begin/commit, rollback on error); `Close()`.
- [ ] `internal/store/queries/*.sql` — named queries; `make sqlc` generates typed methods.
- [ ] Generic helpers over sqlc: pagination (`ListPaginated`), soft-delete (`UPDATE ... is_active=false`), filter whitelist. Service layer with pre/post hooks (plain funcs, not decorators).
- [ ] Pagination math → `response.Paginated` (`total_pages`, `has_next`, `has_prev`).

**Key signatures**
```go
func NewPool(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, error)
func Atomic(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error

type Service[T any] struct{ /* queries, hooks */ }
func (s *Service[T]) Create(ctx, in T) (T, error)   // pre → insert → post
func (s *Service[T]) SoftDelete(ctx, id int64) error
```

**Exit:** one real table + queries + service behind a CRUD route; soft-delete verified; `Atomic` rolls back on injected error. Integration test hits real Postgres via `DATABASE_URL`.

## Phase 4 — Resilience (Valkey)  · size: L

**Tasks**
- [ ] `internal/valkey` — `Client(alias string) (*redis.Client, error)` cached; parse `VALKEY_URL`; `Ping` health; empty URL ⇒ callers use in-memory impls.
- [ ] `resilience/retry` — `Do(ctx, fn, opts) error`; delay `min(base·2^n, max)·U(0.5,1.5)`; stop on non-retryable (`TripsBreaker==false` and not transient).
- [ ] `resilience/breaker` — `CLOSED/OPEN/HALF_OPEN`; `Breaker` interface; memory + valkey impls behind `provider.Get(name)`; `Call(ctx, fn)` raises `ServiceUnavailableError` when OPEN; counts only breaker-tripping errors.
- [ ] `resilience/cache` — `Cache` interface (`Get/Set/Delete`), memory + valkey, fail-open, TTL, dataset-version invalidation.
- [ ] `resilience/throttle` — sliding-window `Check(ctx, id, limit, window) (Result, error)`; scopes (user-tier/burst/global/endpoint/ip); `ParseRate("100/min")`; feeds Phase-2 rate-limit headers via a gin dependency `RateLimit(scope, rate)`.
- [ ] `resilience/registry` — per-service config from `Config`; `Resilient(ctx, name, fn)` = breaker(retry(fn)).

**Key signatures**
```go
func Do(ctx context.Context, fn func() error, o Options) error
type Breaker interface { Call(ctx context.Context, fn func() error) error; State() State }
func Resilient(ctx context.Context, name string, fn func() error) error
func ParseRate(s string) (limit int, window time.Duration, err error)
```

**Exit:** an endpoint rate-limited via Valkey (returns 429 + `X-RateLimit-*`); a flaky call wrapped in `Resilient` opens the breaker after `BREAKER_FAIL_THRESHOLD`. Integration test against real Valkey.

## Phase 5 — Outbound clients & utils  · size: M

**Tasks**
- [ ] `internal/httpclient` — pooled `*http.Client` (tuned `Transport`); per-call auth (bearer/basic/apikey/none); error mapping (timeout→`ExternalTimeoutError`, 5xx→`TransientError`, 4xx→`ExternalServiceError`); `Resilient` applied per service.
- [ ] SSRF guard — validate the resolved IP **inside the transport `DialContext`** (on the `addr` being dialed) to defeat DNS-rebinding/TOCTOU; reject private/loopback/link-local/metadata IPs. `AssertPublicURL(url)` stays as a cheap early reject; `SafeHost(url)` for logging.
- [ ] Log sanitization — `Sanitize(v)` masks sensitive keys (`password|token|secret|key|authorization`), truncates long strings/lists. Reused by Phase 7 body capture.
- [ ] Crypto/S3/SES — **deferred** until a feature needs them.

**Exit:** a resilient, SSRF-guarded outbound call to a real upstream; request/response logged with sanitized bodies. Test asserts a private-IP URL is rejected.

## Phase 6 — Lifecycle  · size: S

**Tasks**
- [ ] `internal/health` — `HealthRouter` (db `SELECT 1`) + `ReadinessRouter` (db + valkey + throttle); 200 all-pass, 503 any-fail, envelope-shaped body.
- [ ] Graceful shutdown — `signal.NotifyContext` (SIGINT/SIGTERM), `http.Server.Shutdown`, then **drain the apilog collector** (Phase 7), then **flush telemetry** (Phase 8: spans + metrics), then dispose pgx pool + valkey clients. Order: stop accepting → drain in-flight → flush audit buffer → flush OTel → close pools.

**Exit:** `/health` + `/ready` return correct status under a dependency-down simulation; clean shutdown drains in-flight requests, flushes the audit buffer, and flushes pending spans/metrics.

## Phase 7 — Async API audit log (non-blocking)  · size: M

**Goal:** capture inbound (and later outbound) request/response metadata
with **zero DB work in the request path** — so it never caps throughput.
Replaces the Python `@log_inbound_request` fire-and-forget with a
bounded-queue + batch-writer design that is safe at 100K RPS.

**Design**
- **Capture (hot path, cheap):** `middleware.APILog` builds a `Record`
  after the handler returns (method, url, status, request_id, duration_ms,
  request/response sizes, error, timestamp) and calls
  `collector.Enqueue(rec)` — a **non-blocking** send to a bounded channel.
- **Backpressure = drop, never block:** channel full ⇒ drop the record,
  increment a `dropped_total` counter (exposed for metrics). The request
  is never delayed by audit load. Bounded memory by construction.
- **Sampling:** always log non-2xx; sample `APILOG_SAMPLE_2XX` fraction of
  2xx (deterministic by hashing request_id, not RNG per request).
- **Batch writer:** `APILOG_WORKERS` goroutines drain the channel into a
  slice; flush on `APILOG_BATCH_SIZE` **or** `APILOG_FLUSH_MS`, whichever
  first, via `pgx.CopyFrom` (bulk) on a **dedicated small pool** separate
  from the app pool — audit writes never contend for app connections.
- **Pluggable sink (`APILOG_SINK`):** `postgres` (batch INSERT/COPY,
  default) or `queue` (publish to Kafka/NATS; a downstream consumer lands
  it in ClickHouse/columnar). Interface makes the swap config, not code —
  the escape hatch for 100K RPS where Postgres audit saturates.
- **Body capture:** opt-in (`APILOG_CAPTURE_BODY`), size-capped
  (`APILOG_MAX_BODY_BYTES`), sanitized via Phase-5 `Sanitize`.
- **Retention:** daily-partitioned table + drop old partitions (cheaper
  than row-level TTL delete at scale); `APILOG_TTL_DAYS` sets the window.

**Tasks**
- [ ] `migrations/000N_api_log.sql` — partitioned table `api_log` (by day), indexes on `request_id`, `timestamp`, `status`; include a `DEFAULT` partition.
- [ ] Partition provisioning — `ensureAPILogPartitions(ctx, days)` at startup + daily: `CREATE TABLE … PARTITION OF api_log … IF NOT EXISTS` for the next N days (so inserts never fail on a day roll) and drop partitions older than `APILOG_TTL_DAYS`.
- [ ] `internal/apilog/record.go` — `Record` struct + `Direction` enum.
- [ ] `internal/apilog/collector.go` — bounded `chan Record`, `Enqueue` (non-blocking select-default drop + counter), `Dropped() uint64`.
- [ ] `internal/apilog/worker.go` — drain + batch + timed flush; `Sink` interface.
- [ ] `internal/apilog/sink_postgres.go` — `pgx.CopyFrom` bulk insert on dedicated pool.
- [ ] `internal/apilog/sink_queue.go` — stub/impl publishing to Kafka/NATS (behind interface; real impl when 100K path is needed).
- [ ] `middleware.APILog(collector, cfg)` — build Record post-handler, sample, enqueue. Wire into Phase-2 stack (innermost, after handler).
- [ ] Shutdown drain hook consumed by Phase 6.

**Key signatures**
```go
type Record struct {
    Direction  string; Method, URL string; Status int
    RequestID  string; DurationMS  int64
    ReqBytes, RespBytes int
    ErrType, ErrMsg string; Timestamp time.Time
    ReqBody, RespBody []byte // optional, capped+sanitized
}
type Sink interface { Write(ctx context.Context, batch []Record) error }
type Collector struct { /* ch chan Record, dropped atomic.Uint64 */ }
func (c *Collector) Enqueue(r Record)          // non-blocking; drops on full
func (c *Collector) Run(ctx context.Context)   // worker pool, batches to Sink
func (c *Collector) Drain(timeout time.Duration) error // flush on shutdown
```

**Exit:** under a load test, request p95 is unchanged with audit ON vs OFF
(proves no DB in hot path); forcing the channel full increments
`dropped_total` and does **not** raise latency; batch rows land via
`CopyFrom`; SIGTERM flushes the remaining buffer within the drain timeout.
Integration test against real Postgres.

## Phase 8 — Observability (traces, metrics, logs → Grafana)  · size: L

**Goal:** full telemetry — distributed **traces** (Tempo), **metrics**
(Prometheus, RED per endpoint), and **logs** (Loki), all emitted via
**OpenTelemetry OTLP** to an **OTel Collector** and visualized in
**Grafana** with cross-signal correlation (`trace_id` links logs↔traces,
exemplars link metrics→traces).

**App instrumentation (`internal/telemetry`)**
- [ ] `Init(ctx, cfg) (shutdown func(context.Context) error, err error)` — build `resource` (`service.name/version`, `deployment.environment`), a `TracerProvider` (OTLP gRPC exporter, batch, parent-based ratio sampler) and a `MeterProvider` (OTLP, periodic reader). Set global providers + W3C `TraceContext`+`Baggage` propagators. `OTEL_ENABLED=false` ⇒ no-op providers, stdout logs only.
- [ ] `otelgin.Middleware(service)` in the Phase-2 stack → automatic server span per request + HTTP server RED metrics (`http.server.request.duration` histogram, request count, in-flight). Install **after** RequestID so spans carry `request_id`.
- [ ] Log correlation: `otelslog` bridge ships slog records over OTLP to Loki; also add a slog `ReplaceAttr`/handler that injects `trace_id`+`span_id` from the active span into every stdout line (so local logs correlate too). `request_id` already present (Phase 1).
- [ ] Custom app metrics (via a `metric.Meter`): `apilog.dropped_total`, `breaker.state` (gauge per service), `cache.hits/misses`, `throttle.rejected_total`, plus pgxpool stats (acquired/idle/total conns) polled into gauges.
- [ ] Exemplars: attach `trace_id` to the request-duration histogram so Grafana can jump metric→trace.
- [ ] Shutdown: Phase-6 drain calls `telemetry` shutdown (flush spans + metrics) **after** the apilog drain, before closing pools.

**Collector + backends (`deploy/observability/`)**
- [ ] `otel-collector-config.yaml` — receivers: `otlp` (grpc/http); processors: `batch`, `memory_limiter`, `resource`; exporters: `otlp/tempo` (traces), `prometheus` or `prometheusremotewrite` (metrics), `loki` (logs); 3 pipelines wired.
- [ ] `tempo.yaml`, `loki-config.yaml`, `prometheus.yml` (scrape the collector's `prometheus` exporter; `--enable-feature=exemplar-storage` on).
- [ ] `grafana/provisioning/datasources/*.yaml` — Prometheus, Tempo, Loki; Tempo↔Loki `tracesToLogs`/`logsToTraces` by `trace_id`; Prometheus exemplars → Tempo.
- [ ] `grafana/dashboards/*.json` — **RED** (rate/errors/duration per route), latency **heatmap** (histogram), throughput (req/s), error-rate %, resource stats (goroutines, GC, pgx pool, valkey), apilog drops + breaker states. Provisioned so they load on boot.
- [ ] `docker-compose.observability.yml` (above); Makefile `obs-up`/`obs-down`.

**Key signatures**
```go
func Init(ctx context.Context, cfg *config.Config) (shutdown func(context.Context) error, err error)
func Meter() metric.Meter           // for custom instruments
// middleware: r.Use(otelgin.Middleware(cfg.OtelServiceName))
```

**Exit:** hit an endpoint → span appears in Grafana/Tempo; its logs are
reachable from the trace by `trace_id` (Loki); RED panels populate in
Grafana from Prometheus; a metric exemplar jumps to its trace. `OTEL_ENABLED=false`
runs the app with zero exporters (no Collector needed). Integration test
asserts a span is exported to an in-test OTLP receiver.

---

## ADR stubs (draft as each phase lands)

- `0001-sql-layer.md` — pgx + sqlc over GORM (control + compile-time-checked queries vs ORM parity).
- `0002-config-and-secrets.md` — caarlos0/env + godotenv; cloud secrets via env injection + pre-load seam.
- `0003-resilience-backend.md` — Valkey + go-redis; provider pattern with in-memory fallback.
- `0004-error-envelope.md` — single response envelope + AppError hierarchy + status registry.
- `0005-api-audit-log.md` — async bounded-queue + batch writer, drop-on-overflow, pluggable sink; why synchronous per-request audit is a throughput liability at scale.
- `0006-observability.md` — OpenTelemetry OTLP → single Collector → Tempo/Prometheus/Loki → Grafana; why OTel-for-all-signals over per-signal agents, and vendor-neutral exporters over direct client libs.

## Deferred (out of scope now)

- `auth/` — API-key gen + HMAC-SHA256 lookup + encrypted copy. Add when endpoints need protection.
- `utils/s3`, `utils/ses`, `utils/crypto`, `utils/pii` — add per feature.
- AWS Secrets Manager fetch — seam exists in Phase 0; implement when deploying to cloud.
- `queue` sink real impl (Kafka/NATS + ClickHouse) — interface exists in Phase 7; build when Postgres audit saturates on the 100K path.
- ~~GitHub Actions CI — after `git init` + remote.~~ Done: `.github/workflows/ci.yml` (build · vet · test · golangci-lint on push/PR). Integration-test job (Postgres + Valkey `services:`) added when the `integration` tag lands in Phase 3/4.

## Build order — earn the complexity (the ladder is the spine)

The phase sections above describe **how** each subsystem is built. They are
**not** the build order. Build order follows the **RPS ladder**: a subsystem
is built, wired onto the hot path, and proven at the rung its bottleneck
appears — never before. The one hard prerequisite is the **benchmark harness
first**, so every rung has a baseline to measure against.

### Restart note

This mainline was recut from the pre-Valkey baseline (`7e9b706`, Phases 0–3).
The earlier Phase 4–5 code (Valkey resilience, httpclient) is preserved in tag
`phase5-built` and re-introduced **at its rung**, each time behind a k6 run
that proves the failure first and the fix second. Done work is kept, not
discarded — only its *entry point on the hot path* is re-sequenced.

### Rung-ordered build order

| Order | Rung | Build / wire | From phase | Status |
|---|---|---|---|---|
| 1 | 1 | Config, edge, Postgres CRUD on `items` | 0,1,2,3 | **done** (baseline) |
| 2 | 1 | **Benchmark harness** (k6 smoke/load/stress/spike/soak) + per-layer timing + rung-1 logbook baseline | new | next |
| 3 | 2 | Indexes — kill Seq Scans | — | |
| 4 | 3 | Pool tuning, hot-query EXPLAIN | — | |
| 5 | 4 | **Cache-aside** (Valkey cache from `phase5-built`) on the hot read | 4 | |
| 6 | 4→6 | **Cache hardening** — TTL jitter → singleflight → XFetch → bloom → hot-key (each symptom-gated) | new | |
| 7 | 5 | **Observability** (OTel → Tempo/Prometheus/Loki/Grafana) — single binary goes blind here | 8 | |
| 8 | 5 | **Rate-limit throttle** (edge hardening) — bounded per-IP map (fixes memory-leak) | 4 | |
| 9 | 5 | **Async audit log** — proven off-path via the ON/OFF matrix | 7 | |
| 10 | 5 | **Lifecycle** — health/readiness + graceful shutdown (drains audit + telemetry) | 6 | |
| 11 | 5 | Cloud: replicas + LB + PgBouncer → 10K rps | — | |
| 12 | 6 | Shard/partition + CDN + audit `queue` sink → 100K rps | — | |
| — | n/a | **Outbound resilience** (breaker/retry/registry + httpclient) | 5 | consumer-driven, off-ladder |

### Rules

- **Harness before infra.** No subsystem is wired onto the hot path until a
  rung-1 baseline exists in `docs/benchmark-logbook.md`.
- **No hot-path wiring without a proof.** Each cache/hardening/observability
  addition ships with a k6 before/after showing the failure and the cure.
- Ship each addition behind a real endpoint; do not wire a subsystem no route
  exercises yet.
- Each rung: code + integration test (real PG/Valkey) + logbook row + ADR if
  it made a decision.
- 100K RPS is infra work (replicas + LB + pgbouncer + read replicas + cache
  hit-rate) layered on this stateless base; the audit log's async design keeps
  it from becoming the one stateful bottleneck.
```
