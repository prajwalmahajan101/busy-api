# Contributing

Thanks for your interest in `busy-api`. This is a **scaling testbed**, not a
general-purpose framework — the most welcome contributions keep it small and
honest: bug fixes, benchmark reproductions, docs, and focused improvements that
stay faithful to the *earn-the-complexity* rule below.

## The one rule that matters here

**No optimization without a reproduced failure and a benchmark.** Every scaling
technique in this repo was added only after a k6 run proved the bottleneck on the
current code, and each ships with a before/after row in
[`docs/benchmark-logbook.md`](docs/benchmark-logbook.md). A PR that adds a cache,
index, pool tweak, or any "performance" change **must** include:

1. a k6 scenario (or an existing `make load-*` target) that reproduces the problem,
2. the fix,
3. a re-run proving it, and
4. the logbook before/after row.

Unproven hardening is reverted, not kept. If you can't reproduce the failure, the
change isn't earned yet.

## Getting started

```bash
git clone https://github.com/prajwalmahajan101/busy-api.git
cd busy-api
cp .env.example .env            # set DATABASE_URL
docker compose up -d            # Valkey
# Postgres: your own, or:
#   docker run -d --name busy-api-postgres \
#     -e POSTGRES_USER=root -e POSTGRES_PASSWORD=root_password -e POSTGRES_DB=busyapi \
#     -p 5432:5432 postgres:16-alpine
make migrate-up
make run                        # API on :8000
```

See [`docs/ROADMAP.md`](docs/ROADMAP.md) for the RPS ladder and
[`docs/adr/`](docs/adr/) for the decisions behind the design.

## Workflow

1. **Branch** off `main` — never commit to `main` directly. Use a descriptive
   name, e.g. `feat/<topic>`, `fix/<topic>`, `docs/<topic>`.
2. **Make the change.** Match the surrounding code; keep handlers thin, business
   logic in services, SQL in `sqlc` queries. Take the smallest change that works.
3. **Verify locally** before opening a PR:
   ```bash
   go build ./...
   go vet ./...
   go test ./...                                   # unit
   make lint                                       # golangci-lint
   # integration (needs Postgres + Valkey up):
   go test -tags=integration ./internal/items/...
   ```
4. **Open a PR** into `main`. CI must be green (build/vet/test, golangci-lint)
   before merge.

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<optional scope>): <subject>
```

- Types: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `perf`.
- Subject in the imperative mood, ≤72 chars, no trailing period.
- Keep commits **atomic** — one logical change each.

Example: `perf(items): cache the list endpoint behind a versioned key`

## Code style

- **Format:** `gofmt` (CI rejects unformatted code).
- **Errors:** raise specific, typed errors with context (`internal/errs`); no
  silent `catch`, no fallback values that hide failures.
- **Config:** add new env vars to `internal/config/config.go` (with an
  `envDefault`) and mirror them in `.env.example`.
- **Generated code:** regenerate and commit after query changes — `make sqlc`.
- **Tests:** prefer integration/e2e over heavily-mocked unit tests. Integration
  tests use the `//go:build integration` tag and need real Postgres + Valkey.

## Architecture decisions

Non-trivial design changes (new dependency, schema migration, protocol change, a
new scaling tier) should add an ADR in [`docs/adr/`](docs/adr/) using the
Context / Decision / Consequences / Usage template, and update the ADR index.

## Reporting issues

Open a GitHub issue with a clear title, expected vs. actual, and a minimal
reproduction — for performance issues, the `make load-*` invocation, the server
knobs, and the k6 summary (p50/p95/p99, `server_repo_ms`, achieved rps).

## License

By contributing, you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
