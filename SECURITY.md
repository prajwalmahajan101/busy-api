# Security Policy

## Supported versions

This is a `0.x` scaling testbed under active development. Security fixes land on
the latest `0.x` release and `main`.

| Version | Supported |
|---|---|
| latest `0.x` / `main` | ✅ |
| older tags | ❌ |

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report privately via GitHub's **[Report a vulnerability](https://github.com/prajwalmahajan101/busy-api/security/advisories/new)**
(Security → Advisories). If that is unavailable, open a minimal issue asking to
be contacted privately — without any exploit detail.

Please include:

- affected version / commit,
- a description and, where possible, a minimal reproduction,
- impact assessment (what an attacker can do).

### What to expect

- Acknowledgement within a few days.
- An initial assessment and, if accepted, a fix on `main` plus a patched release.
- Credit in the release notes if you'd like it.

## Scope notes

This is a benchmark testbed, not a hosted service. A few deliberate ceilings are
documented rather than hidden:

- **Secrets** come from the environment (`.env` is git-ignored; `.env.example`
  ships only placeholders) — never commit real secrets.
- **Parameterized queries only** via `sqlc`; user-supplied sort columns are
  whitelisted before interpolation.
- The **outbound resilience / SSRF-guarded HTTP client** is preserved in the
  `legacy` branch / `phase5-built` tag and re-introduced at its rung — it is not
  on the current hot path.
- Request bodies are size-capped at the edge; rate limiting (per-IP throttle) is
  a planned rung-5 task, not yet wired.
