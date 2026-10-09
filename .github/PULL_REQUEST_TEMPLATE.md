<!-- Keep PRs focused and atomic. See CONTRIBUTING.md. -->

## What

<!-- What does this change do, and why? -->

## Type

- [ ] feat
- [ ] fix
- [ ] refactor
- [ ] perf
- [ ] docs
- [ ] test
- [ ] chore

## Checklist

- [ ] Branched off `main`; commits follow Conventional Commits
- [ ] `go build ./... && go vet ./... && go test ./...` pass; `make lint` clean
- [ ] Integration tests updated/passing where relevant (`-tags=integration`)
- [ ] **Performance change?** Includes a reproduced-failure k6 scenario, the fix,
      a re-run, and a before/after row in `docs/benchmark-logbook.md`
- [ ] Added an ADR under `docs/adr/` for any non-trivial design decision
- [ ] Updated docs / `.env.example` if needed

## Notes

<!-- Breaking changes, migration steps, follow-ups, or anything reviewers should know. -->
