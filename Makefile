# busy-api Makefile
# All targets are phony (no output files match the target names).
#
# Parameterisation (override on the command line or via .env):
#   BASE_URL  — base URL for load tests          (default: http://localhost:8000)
#   RPS       — target requests/sec for load.js  (default: 100)
#   VUS       — virtual users                    (default: 10)
#   DURATION  — test duration string             (default: 30s)

# ---------------------------------------------------------------------------
# Variables
# ---------------------------------------------------------------------------

BINARY      := bin/server
AIR_BIN_DIR := tmp/bin

# Entry point
MAIN := ./cmd/server

# Go toolchain flags
GOFLAGS ?=

# Load-test parameters
BASE_URL  ?= http://localhost:8000
RPS       ?= 100
VUS       ?= 10
DURATION  ?= 30s
N         ?= 100000

# Migrations
MIGRATIONS_DIR := migrations
DB_DRIVER      := postgres

# ---------------------------------------------------------------------------
# Phony declarations
# ---------------------------------------------------------------------------

.PHONY: help \
        run build \
        test lint \
        migrate-up migrate-down \
        sqlc \
        dev \
        compose-up compose-down \
        obs-up obs-down \
        load-smoke load load-stress load-spike load-soak load-matrix load-seed

# ---------------------------------------------------------------------------
# Default target
# ---------------------------------------------------------------------------

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# help — list all targets with descriptions
# ---------------------------------------------------------------------------

help: ## Show this help message
	@echo ""
	@echo "Usage: make <target> [VAR=value ...]"
	@echo ""
	@echo "Targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'
	@echo ""
	@echo "Load-test parameters (env overrides):"
	@echo "  BASE_URL  $(BASE_URL)"
	@echo "  RPS       $(RPS)"
	@echo "  VUS       $(VUS)"
	@echo "  DURATION  $(DURATION)"
	@echo ""

# ---------------------------------------------------------------------------
# build — compile the server binary
# ---------------------------------------------------------------------------

build: ## Compile the binary to bin/server (also creates tmp/bin/ for air)
	@mkdir -p $(dir $(BINARY)) $(AIR_BIN_DIR)
	$(GOFLAGS) go build -o $(BINARY) $(MAIN)

# ---------------------------------------------------------------------------
# run — build then execute the server
# ---------------------------------------------------------------------------

run: build ## Build and run the server
	./$(BINARY)

# ---------------------------------------------------------------------------
# test — run all tests
# ---------------------------------------------------------------------------

test: ## Run all tests (unit; use build tag 'integration' for integration tests)
	go test $(GOFLAGS) ./...

# ---------------------------------------------------------------------------
# lint — static analysis via golangci-lint
# ---------------------------------------------------------------------------

lint: ## Run golangci-lint
	golangci-lint run ./...

# ---------------------------------------------------------------------------
# migrate-up / migrate-down — goose migrations
# ---------------------------------------------------------------------------

migrate-up: ## Apply all pending Goose migrations (requires DATABASE_URL)
	@test -n "$(DATABASE_URL)" || \
		(echo "ERROR: DATABASE_URL is not set"; exit 1)
	goose -dir $(MIGRATIONS_DIR) $(DB_DRIVER) "$(DATABASE_URL)" up

migrate-down: ## Roll back the last Goose migration (requires DATABASE_URL)
	@test -n "$(DATABASE_URL)" || \
		(echo "ERROR: DATABASE_URL is not set"; exit 1)
	goose -dir $(MIGRATIONS_DIR) $(DB_DRIVER) "$(DATABASE_URL)" down

# ---------------------------------------------------------------------------
# sqlc — regenerate typed query code
# ---------------------------------------------------------------------------

sqlc: ## Run sqlc generate (reads sqlc.yaml)
	sqlc generate

# ---------------------------------------------------------------------------
# dev — hot-reload development server via air
# ---------------------------------------------------------------------------

dev: ## Start the development server with hot-reload (requires air)
	air

# ---------------------------------------------------------------------------
# Docker Compose — main stack (Valkey)
# ---------------------------------------------------------------------------

compose-up: ## Start the main docker-compose stack (Valkey) in detached mode
	docker compose up -d

compose-down: ## Stop and remove the main docker-compose stack
	docker compose down

# ---------------------------------------------------------------------------
# Docker Compose — observability stack
# ---------------------------------------------------------------------------

obs-up: ## Start the observability stack (collector, tempo, loki, prometheus, grafana)
	docker compose -f docker-compose.observability.yml up -d

obs-down: ## Stop and remove the observability stack
	docker compose -f docker-compose.observability.yml down

# ---------------------------------------------------------------------------
# Load tests (k6) — targets used by the benchmark matrix
# ---------------------------------------------------------------------------

load-seed: ## Seed the items table with N rows (default 100000) for load tests
	@test -n "$(DATABASE_URL)" || \
		(echo "ERROR: DATABASE_URL is not set"; exit 1)
	psql "$(DATABASE_URL)" -v n=$(N) -f loadtest/seed.sql

load-smoke: ## Run smoke test (1 VU, 30 s) against BASE_URL
	k6 run loadtest/smoke.js -e BASE_URL=$(BASE_URL)

load: ## Run ramp load test at target RPS against BASE_URL
	k6 run loadtest/load.js \
		-e BASE_URL=$(BASE_URL) \
		-e RPS=$(RPS) \
		-e VUS=$(VUS) \
		-e DURATION=$(DURATION)

load-stress: ## Ramp past target RPS to find the break-point ceiling
	k6 run loadtest/stress.js \
		-e BASE_URL=$(BASE_URL) \
		-e RPS=$(RPS) \
		-e VUS=$(VUS) \
		-e DURATION=$(DURATION)

load-spike: ## Sudden burst test (0 → peak → 0)
	k6 run loadtest/spike.js \
		-e BASE_URL=$(BASE_URL) \
		-e RPS=$(RPS) \
		-e VUS=$(VUS)

load-soak: ## 30-minute sustained load; watches goroutines, heap, audit drops
	k6 run loadtest/soak.js \
		-e BASE_URL=$(BASE_URL) \
		-e RPS=$(RPS) \
		-e VUS=$(VUS)

load-matrix: ## Run all four OTEL×APILOG combinations and append delta summary to logbook
	@echo "=== Matrix run: OTEL=false APILOG=false ===" && \
	OTEL_ENABLED=false APILOG_ENABLED=false \
		k6 run loadtest/load.js -e BASE_URL=$(BASE_URL) -e RPS=$(RPS) && \
	echo "=== Matrix run: OTEL=true APILOG=false ===" && \
	OTEL_ENABLED=true APILOG_ENABLED=false \
		k6 run loadtest/load.js -e BASE_URL=$(BASE_URL) -e RPS=$(RPS) && \
	echo "=== Matrix run: OTEL=false APILOG=true ===" && \
	OTEL_ENABLED=false APILOG_ENABLED=true \
		k6 run loadtest/load.js -e BASE_URL=$(BASE_URL) -e RPS=$(RPS) && \
	echo "=== Matrix run: OTEL=true APILOG=true ===" && \
	OTEL_ENABLED=true APILOG_ENABLED=true \
		k6 run loadtest/load.js -e BASE_URL=$(BASE_URL) -e RPS=$(RPS)
