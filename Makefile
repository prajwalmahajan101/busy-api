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
PHASE_S   ?= 30
KEYS      ?= 1000
ITEM_TTL_S ?= 20
WARM_S    ?= 3
DURATION_S ?= 40
HOT_ID    ?= 2

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
        load-smoke load load-stress load-spike load-soak load-matrix load-seed load-cache \
        load-cache-resilience load-cache-avalanche load-cache-stampede

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

load-cache: ## Rung-4 cache-aside hot-read proof (GET /items/:id over KEYS working set)
	k6 run loadtest/cache_read.js \
		-e BASE_URL=$(BASE_URL) \
		-e RPS=$(RPS) \
		-e VUS=$(VUS) \
		-e DURATION=$(DURATION)

load-cache-resilience: ## Rung-4b proof: drop/restore Valkey mid-load, per-phase p95 + db_reads (run with DB_MAX_CONNS=1)
	k6 run loadtest/cache_resilience.js \
		-e BASE_URL=$(BASE_URL) \
		-e RPS=$(RPS) \
		-e VUS=$(VUS) \
		-e PHASE_S=$(PHASE_S) & \
	K6_PID=$$!; \
	sleep $(PHASE_S); echo ">>> stopping valkey (warm -> outage)"; docker compose stop valkey; \
	sleep $(PHASE_S); echo ">>> starting valkey (outage -> recovery)"; docker compose start valkey; \
	wait $$K6_PID

load-cache-avalanche: ## Rung-4c T23 proof: synchronized-TTL avalanche (run API with CACHE_ITEM_TTL_S=20 CACHE_L1_ENABLED=false [DB_MAX_CONNS=1])
	@echo ">>> flushing Valkey (clean baseline)"; docker compose exec -T valkey valkey-cli flushall >/dev/null 2>&1 || docker exec valkey valkey-cli flushall >/dev/null
	k6 run loadtest/cache_avalanche.js \
		-e BASE_URL=$(BASE_URL) \
		-e RPS=$(RPS) \
		-e VUS=$(VUS) \
		-e KEYS=$(KEYS) \
		-e ITEM_TTL_S=$(ITEM_TTL_S) & \
	K6_PID=$$!; \
	echo "t(s)  dbsize  db_reads/s (Valkey keyspace_misses delta — spike at ~TTL = avalanche)"; \
	PREV=$$(docker exec valkey valkey-cli info stats 2>/dev/null | tr -d '\r' | awk -F: '/keyspace_misses/{print $$2}'); \
	for t in $$(seq 0 $$(( $(ITEM_TTL_S) * 2 + $(WARM_S) + 2 )) ); do \
		DBSIZE=$$(docker exec valkey valkey-cli dbsize 2>/dev/null); \
		CUR=$$(docker exec valkey valkey-cli info stats 2>/dev/null | tr -d '\r' | awk -F: '/keyspace_misses/{print $$2}'); \
		printf "%4d  %6s  %s\n" "$$t" "$$DBSIZE" "$$(( $${CUR:-0} - $${PREV:-0} ))"; \
		PREV=$$CUR; sleep 1; \
	done; \
	wait $$K6_PID

load-cache-stampede: ## Rung-4c T25 proof: one hot key's expiry → M duplicate DB reads (run API with CACHE_ITEM_TTL_S=5 CACHE_L1_ENABLED=false DB_MAX_CONNS=1)
	@echo ">>> flushing Valkey (clean baseline)"; docker compose exec -T valkey valkey-cli flushall >/dev/null 2>&1 || docker exec valkey valkey-cli flushall >/dev/null
	k6 run loadtest/cache_stampede.js \
		-e BASE_URL=$(BASE_URL) \
		-e RPS=$(RPS) \
		-e VUS=$(VUS) \
		-e HOT_ID=$(HOT_ID) \
		-e DURATION_S=$(DURATION_S) & \
	K6_PID=$$!; \
	echo "t(s)  db_reads/s (Postgres items.idx_scan delta — ACTUAL DB reads, not Valkey misses: singleflight collapses a miss burst to ~1 DB read, so measure the DB, not the cache)"; \
	Q="select idx_scan from pg_stat_user_tables where relname='items'"; \
	TOTAL=0; MAX=0; \
	PREV=$$(psql "$(DATABASE_URL)" -tAc "$$Q" 2>/dev/null | tr -d ' '); \
	for t in $$(seq 0 $$(( $(DURATION_S) + 1 )) ); do \
		CUR=$$(psql "$(DATABASE_URL)" -tAc "$$Q" 2>/dev/null | tr -d ' '); \
		D=$$(( $${CUR:-0} - $${PREV:-0} )); TOTAL=$$(( TOTAL + D )); [ $$D -gt $$MAX ] && MAX=$$D; \
		printf "%4d  %s\n" "$$t" "$$D"; \
		PREV=$$CUR; sleep 1; \
	done; \
	echo ">>> total DB reads for ONE key over run: $$TOTAL ; peak stampede M (one expiry): $$MAX ; ideal = 1 per expiry"; \
	wait $$K6_PID

load-cache-penetration: ## Rung-4c T28 proof: flood of absent ids → DB read rate tracks RPS (run API with CACHE_BLOOM_ENABLED=false CACHE_L1_ENABLED=false DB_MAX_CONNS=1)
	@echo ">>> flushing Valkey (clean baseline)"; docker compose exec -T valkey valkey-cli flushall >/dev/null 2>&1 || docker exec valkey valkey-cli flushall >/dev/null
	k6 run loadtest/cache_penetration.js \
		-e BASE_URL=$(BASE_URL) \
		-e RPS=$(RPS) \
		-e VUS=$(VUS) \
		-e DURATION_S=$(DURATION_S) & \
	K6_PID=$$!; \
	echo "t(s)  db_reads/s (Postgres items.idx_scan delta — every absent-id GetItem is one PK scan; before the bloom this tracks RPS, after it should be ~0)"; \
	Q="select idx_scan from pg_stat_user_tables where relname='items'"; \
	TOTAL=0; MAX=0; \
	PREV=$$(psql "$(DATABASE_URL)" -tAc "$$Q" 2>/dev/null | tr -d ' '); \
	for t in $$(seq 0 $$(( $(DURATION_S) + 1 )) ); do \
		CUR=$$(psql "$(DATABASE_URL)" -tAc "$$Q" 2>/dev/null | tr -d ' '); \
		D=$$(( $${CUR:-0} - $${PREV:-0} )); TOTAL=$$(( TOTAL + D )); [ $$D -gt $$MAX ] && MAX=$$D; \
		printf "%4d  %s\n" "$$t" "$$D"; \
		PREV=$$CUR; sleep 1; \
	done; \
	echo ">>> total DB reads over run: $$TOTAL ; peak db_reads/s: $$MAX ; ideal (bloom on) = ~0"; \
	wait $$K6_PID

load-cache-boundary: ## Rung-4c T27 gate: multi-key TTL-boundary DB-read spike (jitter+singleflight ON, L1 off; run API with CACHE_ITEM_TTL_S=20 CACHE_L1_ENABLED=false DB_MAX_CONNS=1)
	@echo ">>> flushing Valkey (clean baseline)"; docker compose exec -T valkey valkey-cli flushall >/dev/null 2>&1 || docker exec valkey valkey-cli flushall >/dev/null
	k6 run loadtest/cache_boundary.js \
		-e BASE_URL=$(BASE_URL) \
		-e RPS=$(RPS) \
		-e VUS=$(VUS) \
		-e KEYS=$(KEYS) \
		-e ITEM_TTL_S=$(ITEM_TTL_S) & \
	K6_PID=$$!; \
	echo "t(s)  db_reads/s (Postgres items.idx_scan delta — spike at ~TTL = boundary residual after jitter+singleflight)"; \
	Q="select idx_scan from pg_stat_user_tables where relname='items'"; \
	TOTAL=0; MAX=0; \
	PREV=$$(psql "$(DATABASE_URL)" -tAc "$$Q" 2>/dev/null | tr -d ' '); \
	for t in $$(seq 0 $$(( $(ITEM_TTL_S) * 2 + $(WARM_S) + 2 )) ); do \
		CUR=$$(psql "$(DATABASE_URL)" -tAc "$$Q" 2>/dev/null | tr -d ' '); \
		D=$$(( $${CUR:-0} - $${PREV:-0} )); TOTAL=$$(( TOTAL + D )); [ $$D -gt $$MAX ] && MAX=$$D; \
		printf "%4d  %s\n" "$$t" "$$D"; \
		PREV=$$CUR; sleep 1; \
	done; \
	echo ">>> total DB reads over run: $$TOTAL ; peak db_reads/s: $$MAX ; ideal (xfetch on) = spread across TTL, no spike"; \
	wait $$K6_PID

load-cache-hotkey: ## Rung-4c T28a gate: single hot key at high RPS with L1 ON (run API with CACHE_L1_ENABLED=true CACHE_ITEM_TTL_S=300 DB_MAX_CONNS=1)
	@echo ">>> flushing Valkey (clean baseline)"; docker compose exec -T valkey valkey-cli flushall >/dev/null 2>&1 || docker exec valkey valkey-cli flushall >/dev/null
	k6 run loadtest/cache_hotkey.js \
		-e BASE_URL=$(BASE_URL) \
		-e RPS=$(RPS) \
		-e VUS=$(VUS) \
		-e HOT_ID=$(HOT_ID) \
		-e DURATION_S=$(DURATION_S) & \
	K6_PID=$$!; \
	echo "t(s)  db_reads/s  valkey_hits/s (L1 absorbs → both should be ~0 after t=1)"; \
	Q="select idx_scan from pg_stat_user_tables where relname='items'"; \
	PREV_DB=$$(psql "$(DATABASE_URL)" -tAc "$$Q" 2>/dev/null | tr -d ' '); \
	PREV_VK=$$(docker exec valkey valkey-cli info stats 2>/dev/null | tr -d '\r' | awk -F: '/keyspace_hits/{print $$2}'); \
	TOTAL_DB=0; MAX_DB=0; TOTAL_VK=0; MAX_VK=0; \
	for t in $$(seq 0 $$(( $(DURATION_S) + 1 )) ); do \
		CUR_DB=$$(psql "$(DATABASE_URL)" -tAc "$$Q" 2>/dev/null | tr -d ' '); \
		CUR_VK=$$(docker exec valkey valkey-cli info stats 2>/dev/null | tr -d '\r' | awk -F: '/keyspace_hits/{print $$2}'); \
		D_DB=$$(( $${CUR_DB:-0} - $${PREV_DB:-0} )); TOTAL_DB=$$(( TOTAL_DB + D_DB )); [ $$D_DB -gt $$MAX_DB ] && MAX_DB=$$D_DB; \
		D_VK=$$(( $${CUR_VK:-0} - $${PREV_VK:-0} )); TOTAL_VK=$$(( TOTAL_VK + D_VK )); [ $$D_VK -gt $$MAX_VK ] && MAX_VK=$$D_VK; \
		printf "%4d  %6s  %s\n" "$$t" "$$D_DB" "$$D_VK"; \
		PREV_DB=$$CUR_DB; PREV_VK=$$CUR_VK; sleep 1; \
	done; \
	echo ">>> total: db_reads=$$TOTAL_DB (peak $$MAX_DB/s), valkey_hits=$$TOTAL_VK (peak $$MAX_VK/s) ; ideal (L1 absorbs) = both ~0 after fill"; \
	wait $$K6_PID

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
