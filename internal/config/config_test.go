package config_test

import (
	"os"
	"testing"

	"github.com/prajwalmahajan101/busyapi/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setEnv sets a key=value pair and registers a cleanup that restores the
// previous value (or unsets it) when the test finishes.
func setEnv(t *testing.T, key, value string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	})
	_ = os.Setenv(key, value)
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
		}
	})
	_ = os.Unsetenv(key)
}

// TestLoad_FailsFastOnMissingDatabaseURL verifies that Load returns a non-nil
// error (and no config) when DATABASE_URL is absent — satisfying R17 "fail fast".
func TestLoad_FailsFastOnMissingDatabaseURL(t *testing.T) {
	unsetEnv(t, "DATABASE_URL")

	cfg, err := config.Load()

	assert.Error(t, err, "Load() must error when DATABASE_URL is missing")
	assert.Nil(t, cfg)
}

// TestLoad_Defaults verifies every optional variable resolves to its documented
// default when only DATABASE_URL is provided.
func TestLoad_Defaults(t *testing.T) {
	setEnv(t, "DATABASE_URL", "postgres://user:pass@localhost:5432/busyapi?sslmode=disable")

	cfg, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Server
	assert.Equal(t, "8000", cfg.Port)
	assert.Equal(t, "local", cfg.Env)

	// Database
	assert.Equal(t, "postgres://user:pass@localhost:5432/busyapi?sslmode=disable", cfg.DatabaseURL)
	assert.Equal(t, 0, cfg.DBMaxConns)
	assert.Equal(t, 5000, cfg.DBConnTimeoutMS)
	assert.Equal(t, 2000, cfg.DBQueryTimeoutMS)

	// Valkey
	assert.Equal(t, "redis://localhost:6379/0", cfg.ValkeyURL)

	// Logging
	assert.Equal(t, "INFO", cfg.LogLevel)
	assert.True(t, cfg.LogJSON)
	assert.Equal(t, "", cfg.LogFile)
	assert.Equal(t, 10, cfg.LogMaxMB)
	assert.Equal(t, 5, cfg.LogBackupCount)

	// HTTP Edge
	assert.Equal(t, int64(1048576), cfg.MaxBodyBytes)
	assert.Empty(t, cfg.CORSOrigins)

	// Retry
	assert.Equal(t, 3, cfg.RetryMax)
	assert.Equal(t, 200, cfg.RetryBaseMS)

	// Circuit Breaker
	assert.Equal(t, 5, cfg.BreakerFailThreshold)
	assert.Equal(t, 30, cfg.BreakerRecoveryS)

	// Audit Log
	assert.True(t, cfg.APILogEnabled)
	assert.Equal(t, "postgres", cfg.APILogSink)
	assert.Equal(t, 10000, cfg.APILogBuffer)
	assert.Equal(t, 2, cfg.APILogWorkers)
	assert.Equal(t, 500, cfg.APILogBatchSize)
	assert.Equal(t, 1000, cfg.APILogFlushMS)
	assert.InDelta(t, 0.1, cfg.APILogSample2XX, 1e-9)
	assert.False(t, cfg.APILogCaptureBody)
	assert.Equal(t, 4096, cfg.APILogMaxBodyBytes)
	assert.Equal(t, 30, cfg.APILogTTLDays)

	// OTel
	assert.True(t, cfg.OtelEnabled)
	assert.Equal(t, "busy-api", cfg.OtelServiceName)
	assert.Equal(t, "dev", cfg.OtelServiceVersion)
	assert.Equal(t, "http://localhost:4317", cfg.OtelExporterEndpoint)
	assert.True(t, cfg.OtelExporterInsecure)
	assert.Equal(t, "parentbased_traceidratio", cfg.OtelTracesSampler)
	assert.InDelta(t, 0.1, cfg.OtelTracesSamplerArg, 1e-9)
	assert.Equal(t, 10000, cfg.OtelMetricExportIntervalMS)
}

// TestLoad_OverridesFromEnv verifies that env vars override the built-in defaults.
func TestLoad_OverridesFromEnv(t *testing.T) {
	setEnv(t, "DATABASE_URL", "postgres://user:pass@db:5432/test")
	setEnv(t, "PORT", "9090")
	setEnv(t, "ENV", "prod")
	setEnv(t, "LOG_LEVEL", "DEBUG")
	setEnv(t, "LOG_JSON", "false")
	setEnv(t, "OTEL_ENABLED", "false")
	setEnv(t, "APILOG_SAMPLE_2XX", "0.5")
	setEnv(t, "CORS_ORIGINS", "https://example.com,https://api.example.com")
	setEnv(t, "DB_MAX_CONNS", "16")

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, "9090", cfg.Port)
	assert.Equal(t, "prod", cfg.Env)
	assert.Equal(t, "DEBUG", cfg.LogLevel)
	assert.False(t, cfg.LogJSON)
	assert.False(t, cfg.OtelEnabled)
	assert.InDelta(t, 0.5, cfg.APILogSample2XX, 1e-9)
	assert.Equal(t, []string{"https://example.com", "https://api.example.com"}, cfg.CORSOrigins)
	assert.Equal(t, 16, cfg.DBMaxConns)
}
