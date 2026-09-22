// Package logging sets up structured JSON logging with request-id propagation,
// mirroring the Python core.utils.logging setup (JSON, UTC, rotation+gzip).
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"

	"github.com/prajwalmahajan101/busyapi/internal/reqcontext"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

// ctxHandler wraps a slog.Handler and injects request_id from context.
// Equivalent to Python's RequestContextFilter.
type ctxHandler struct {
	slog.Handler
}

func (h ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := reqcontext.RequestIDFromContext(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// Setup builds the JSON logger and installs it as slog default.
// Reads env: LOG_LEVEL (default INFO), LOG_JSON (default true),
// LOG_FILE (empty = stdout only). File output rotates + gzips.
func Setup() *slog.Logger {
	opts := &slog.HandlerOptions{
		Level:       parseLevel(os.Getenv("LOG_LEVEL")),
		ReplaceAttr: utcTime,
	}

	var w io.Writer = os.Stdout
	if path := os.Getenv("LOG_FILE"); path != "" {
		w = io.MultiWriter(os.Stdout, &lumberjack.Logger{
			Filename:   path,
			MaxSize:    maxSizeMB(), // megabytes
			MaxBackups: backupCount(),
			Compress:   true, // gzip rotated files
		})
	}

	var base slog.Handler
	if envBool("LOG_JSON", true) {
		base = slog.NewJSONHandler(w, opts)
	} else {
		base = slog.NewTextHandler(w, opts)
	}

	logger := slog.New(ctxHandler{base})
	slog.SetDefault(logger)
	return logger
}

func utcTime(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		a.Value = slog.TimeValue(a.Value.Time().UTC())
	}
	return a
}

func parseLevel(s string) slog.Level {
	switch s {
	case "DEBUG":
		return slog.LevelDebug
	case "WARNING", "WARN":
		return slog.LevelWarn
	case "ERROR", "CRITICAL":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func maxSizeMB() int {
	if v, err := strconv.Atoi(os.Getenv("LOG_MAX_MB")); err == nil && v > 0 {
		return v
	}
	return 10 // 10MB per file before rotate
}

func backupCount() int {
	if v, err := strconv.Atoi(os.Getenv("LOG_BACKUP_COUNT")); err == nil && v > 0 {
		return v
	}
	return 5
}
