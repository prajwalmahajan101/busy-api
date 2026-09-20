# busy-api

Scalable JSON API on Go + Gin + PostgreSQL + Valkey. Ports the reusable
infrastructure of a Python FastAPI core (config, logging, error handling,
persistence, caching, resilience, async audit, observability) into Go.

Designed to run 1K RPS on a single stateless instance and scale to ~100K
RPS by adding replicas behind a load balancer — scale is infra, not code.

## Docs

- `docs/REQUIREMENTS.md` — what the system must do + constraints (source of truth).
- `docs/ROADMAP.md` — phased build sequence (0–8), overhead budget, k6 RPS ladder.
- `docs/adr/` — architecture decisions.

## Stack

Gin · pgx/v5 + sqlc · goose · Valkey (go-redis) · OpenTelemetry → Tempo/Prometheus/Loki → Grafana · k6.

## Quick start

```bash
cp .env.example .env          # set DATABASE_URL
docker compose up -d valkey   # cache/resilience backend
make run                      # boot API on :8000
curl -s localhost:8000/ping | jq
```

Full observability stack (Prometheus/Tempo/Loki/Grafana):

```bash
docker compose -f docker-compose.observability.yml up -d
# Grafana → http://localhost:3000
```

## Status

Bootstrapping — see `docs/ROADMAP.md` for current phase.
