// Package config loads and validates application configuration from environment
// variables. All variables are listed in .env.example. Call Load() once at
// startup; the process exits fast if any required variable is missing.
package config

import (
	"context"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// Config holds every environment variable used by busy-api.
// Tags map directly to the variables documented in .env.example / REQUIREMENTS §9.
type Config struct {
	// -------------------------------------------------------------------------
	// Server
	// -------------------------------------------------------------------------

	// Port is the HTTP listen port (default: 8000).
	Port string `env:"PORT" envDefault:"8000"`

	// Env is the deployment environment: local | staging | prod.
	// Gates HSTS, docs paths, and the secrets source.
	Env string `env:"ENV" envDefault:"local"`

	// -------------------------------------------------------------------------
	// Database (Postgres via pgx)
	// -------------------------------------------------------------------------

	// DatabaseURL is the Postgres DSN, e.g.
	// postgres://user:pass@localhost:5432/busyapi?sslmode=disable
	DatabaseURL string `env:"DATABASE_URL,required"`

	// DBMaxConns is the pgxpool maximum connection count.
	// 0 means the runtime default (4 × CPU cores).
	DBMaxConns int `env:"DB_MAX_CONNS" envDefault:"0"`

	// DBConnTimeoutMS is the pgxpool connect timeout in milliseconds.
	DBConnTimeoutMS int `env:"DB_CONN_TIMEOUT_MS" envDefault:"5000"`

	// DBQueryTimeoutMS is the per-call query deadline in milliseconds, applied
	// as context.WithTimeout on every DB call so a slow Postgres never stalls a
	// goroutine indefinitely.
	DBQueryTimeoutMS int `env:"DB_QUERY_TIMEOUT_MS" envDefault:"2000"`

	// -------------------------------------------------------------------------
	// Valkey / Redis
	// -------------------------------------------------------------------------

	// ValkeyURL is the Valkey (Redis-compatible) connection URL.
	// An empty string disables Valkey and activates in-memory fallbacks.
	ValkeyURL string `env:"VALKEY_URL" envDefault:"redis://localhost:6379/0"`

	// CacheItemTTLS is the cache-aside TTL for single-item reads, in seconds.
	CacheItemTTLS int `env:"CACHE_ITEM_TTL_S" envDefault:"300"`

	// CacheL1Enabled fronts Valkey (L2) with a bounded in-process L1 tier
	// (L1 → L2 → DB) so a Valkey outage does not flood the DB (NFR-R5).
	CacheL1Enabled bool `env:"CACHE_L1_ENABLED" envDefault:"true"`

	// CacheL1Max bounds L1 entries (LRU eviction) so it cannot OOM on an outage.
	CacheL1Max int `env:"CACHE_L1_MAX" envDefault:"10000"`

	// CacheL1TTLS is the short L1 TTL in seconds (L1 may serve slightly stale).
	CacheL1TTLS int `env:"CACHE_L1_TTL_S" envDefault:"30"`

	// CacheTTLJitterPct spreads the L2 (Valkey) Set TTL by ±pct so a burst-warmed
	// working set does not all expire in one instant (cache avalanche, T24).
	CacheTTLJitterPct int `env:"CACHE_TTL_JITTER_PCT" envDefault:"10"`

	// CacheBreakerFailThreshold is the number of Valkey failures before the cache
	// breaker OPENs (skips Valkey, no per-request dial tax).
	CacheBreakerFailThreshold int `env:"CACHE_BREAKER_FAIL_THRESHOLD" envDefault:"5"`

	// CacheBreakerRecoveryS is the cache breaker OPEN→HALF_OPEN probe interval in
	// seconds (self-heal when Valkey returns).
	CacheBreakerRecoveryS int `env:"CACHE_BREAKER_RECOVERY_S" envDefault:"10"`

	// CacheBloomEnabled fronts the hot read with an in-process bloom filter of
	// existing item ids. A flood of absent ids (cache penetration, T28) is answered
	// in-process — "this id cannot exist" short-circuits to 404 before any DB touch,
	// so the DB read rate stays flat instead of tracking attacker RPS 1:1.
	CacheBloomEnabled bool `env:"CACHE_BLOOM_ENABLED" envDefault:"true"`

	// CacheBloomCapacity sizes the bloom for the expected id count at the target
	// false-positive rate. The table may grow past this; FP degrades gracefully
	// (a false positive just falls through to the DB, which answers correctly).
	CacheBloomCapacity int `env:"CACHE_BLOOM_CAPACITY" envDefault:"1000000"`

	// CacheBloomFPRate is the bloom's target false-positive probability.
	CacheBloomFPRate float64 `env:"CACHE_BLOOM_FP_RATE" envDefault:"0.01"`

	// CacheNegTTLS is the negative-cache (tombstone) TTL in seconds: how long a
	// confirmed-absent id is remembered so a repeated bad id (one that passed the
	// bloom as a false positive) does not re-hit the DB. Kept short.
	CacheNegTTLS int `env:"CACHE_NEG_TTL_S" envDefault:"30"`


	// -------------------------------------------------------------------------
	// Logging
	// -------------------------------------------------------------------------

	// LogLevel controls the minimum log level: DEBUG | INFO | WARNING | ERROR.
	LogLevel string `env:"LOG_LEVEL" envDefault:"INFO"`

	// LogJSON emits JSON-structured logs when true, human-readable text when false.
	LogJSON bool `env:"LOG_JSON" envDefault:"true"`

	// LogFile is an optional file path for log rotation + gzip output.
	// Empty means stdout only.
	LogFile string `env:"LOG_FILE" envDefault:""`

	// LogMaxMB is the log file maximum size in megabytes before rotation.
	LogMaxMB int `env:"LOG_MAX_MB" envDefault:"10"`

	// LogBackupCount is the number of rotated log backups to retain.
	LogBackupCount int `env:"LOG_BACKUP_COUNT" envDefault:"5"`

	// -------------------------------------------------------------------------
	// HTTP Edge
	// -------------------------------------------------------------------------

	// MaxBodyBytes is the maximum request body size in bytes.
	// Requests exceeding this are rejected with 413.
	MaxBodyBytes int64 `env:"MAX_BODY_BYTES" envDefault:"1048576"`

	// CORSOrigins is a comma-separated list of allowed CORS origins.
	// Empty disables the CORS middleware entirely.
	CORSOrigins []string `env:"CORS_ORIGINS" envSeparator:","`

	// -------------------------------------------------------------------------
	// Resilience — Retry
	// -------------------------------------------------------------------------

	// RetryMax is the maximum number of retry attempts for outbound calls.
	RetryMax int `env:"RETRY_MAX" envDefault:"3"`

	// RetryBaseMS is the base backoff delay in milliseconds (doubles per attempt with jitter).
	RetryBaseMS int `env:"RETRY_BASE_MS" envDefault:"200"`

	// -------------------------------------------------------------------------
	// Resilience — Circuit Breaker
	// -------------------------------------------------------------------------

	// BreakerFailThreshold is the number of consecutive failures before the
	// circuit breaker opens.
	BreakerFailThreshold int `env:"BREAKER_FAIL_THRESHOLD" envDefault:"5"`

	// BreakerRecoveryS is the number of seconds the breaker stays OPEN before
	// transitioning to HALF_OPEN.
	BreakerRecoveryS int `env:"BREAKER_RECOVERY_S" envDefault:"30"`

	// -------------------------------------------------------------------------
	// Async API Audit Log
	// -------------------------------------------------------------------------

	// APILogEnabled is the master switch for audit logging.
	APILogEnabled bool `env:"APILOG_ENABLED" envDefault:"true"`

	// APILogSink is the audit log sink backend: postgres | queue.
	APILogSink string `env:"APILOG_SINK" envDefault:"postgres"`

	// APILogBuffer is the bounded channel capacity; records are dropped (not
	// blocked) when full.
	APILogBuffer int `env:"APILOG_BUFFER" envDefault:"10000"`

	// APILogWorkers is the number of background drain goroutines.
	APILogWorkers int `env:"APILOG_WORKERS" envDefault:"2"`

	// APILogBatchSize is the number of records to accumulate before flushing to sink.
	APILogBatchSize int `env:"APILOG_BATCH_SIZE" envDefault:"500"`

	// APILogFlushMS is the maximum milliseconds between forced flushes.
	APILogFlushMS int `env:"APILOG_FLUSH_MS" envDefault:"1000"`

	// APILogSample2XX is the fraction of 2xx responses to sample (0.0–1.0).
	// Non-2xx responses are always logged.
	APILogSample2XX float64 `env:"APILOG_SAMPLE_2XX" envDefault:"0.1"`

	// APILogCaptureBody enables capturing sanitised request/response bodies in
	// the audit record.
	APILogCaptureBody bool `env:"APILOG_CAPTURE_BODY" envDefault:"false"`

	// APILogMaxBodyBytes is the maximum bytes to capture from req/resp body
	// (capped and sanitised via Sanitize()).
	APILogMaxBodyBytes int `env:"APILOG_MAX_BODY_BYTES" envDefault:"4096"`

	// APILogTTLDays is the number of days to retain audit records before the
	// daily partition is dropped.
	APILogTTLDays int `env:"APILOG_TTL_DAYS" envDefault:"30"`

	// -------------------------------------------------------------------------
	// OpenTelemetry
	// -------------------------------------------------------------------------

	// OtelEnabled is the master switch for OpenTelemetry exporters.
	// false → no-op providers; stdout logs only; no Collector required.
	OtelEnabled bool `env:"OTEL_ENABLED" envDefault:"true"`

	// OtelServiceName is the OTel resource service.name attribute.
	OtelServiceName string `env:"OTEL_SERVICE_NAME" envDefault:"busy-api"`

	// OtelServiceVersion is the OTel resource service.version attribute.
	OtelServiceVersion string `env:"OTEL_SERVICE_VERSION" envDefault:"dev"`

	// OtelExporterEndpoint is the OTLP gRPC endpoint for the OTel Collector.
	OtelExporterEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT" envDefault:"http://localhost:4317"`

	// OtelExporterInsecure enables plaintext (insecure) OTLP transport for
	// local development.
	OtelExporterInsecure bool `env:"OTEL_EXPORTER_OTLP_INSECURE" envDefault:"true"`

	// OtelTracesSampler is the trace sampler strategy.
	OtelTracesSampler string `env:"OTEL_TRACES_SAMPLER" envDefault:"parentbased_traceidratio"`

	// OtelTracesSamplerArg is the sampling ratio (0.0–1.0).
	OtelTracesSamplerArg float64 `env:"OTEL_TRACES_SAMPLER_ARG" envDefault:"0.1"`

	// OtelMetricExportIntervalMS is the metric export period in milliseconds.
	OtelMetricExportIntervalMS int `env:"OTEL_METRIC_EXPORT_INTERVAL_MS" envDefault:"10000"`
}

// Load reads configuration from the environment, optionally pre-loading a
// local .env file. It always succeeds when the file is absent — suitable for
// production where env vars are injected directly.
//
// The loadCloudSecrets seam (see secrets.go) runs before env.Parse so any
// values fetched from a secrets manager are visible through the same env var
// interface as everything else.
//
// The call fails fast with a descriptive error if any required variable
// (currently only DATABASE_URL) is missing or malformed.
func Load() (*Config, error) {
	// Pre-load .env file; silently ignore if absent (production compatibility).
	_ = godotenv.Load()

	// Allow cloud secrets to populate env vars before parsing.
	if err := loadCloudSecrets(context.Background()); err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}
