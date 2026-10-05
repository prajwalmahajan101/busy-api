import http from "k6/http";
import exec from "k6/execution";
import { Counter } from "k6/metrics";
import { envelopeOK } from "./lib/checks.js";
import { baseThresholds, extractServerTiming } from "./lib/thresholds.js";

// Rung 4b proof — tiered cache + self-healing breaker (T22a, T22b).
//
// Unlike cache_read.js (rung 4a: hit vs cold fail-open, two static runs), this is
// a single steady-state run split into three equal phases while Valkey is dropped
// mid-load and restored:
//   warm     — Valkey up, cache serving (baseline).
//   outage   — Valkey stopped mid-run. On the current single-tier cache this
//              reproduces BOTH 4b failures: per-request dial tax to the dead
//              server (p95 blows past the 50ms budget) AND every miss floods the
//              one DB connection (db_reads{phase:outage} spikes). Fail-open keeps
//              http_req_failed ~0.
//   recovery — Valkey restarted. Current code is slow to settle (no breaker to
//              skip the dial, cold L2). After the L1 tier + breaker land, the
//              re-run must show db_reads{phase:outage} flat and
//              http_req_duration{phase:outage,recovery} back under 50ms.
//
// The Makefile `load-cache-resilience` target stops/starts Valkey on the phase
// boundaries so the outage window is deterministic. Run pool-constrained
// (DB_MAX_CONNS=1) so the DB flood is observable.
//
//   make load-cache-resilience RPS=2000 VUS=300 PHASE_S=30
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 2000);
const VUS = Number(__ENV.VUS || 300);
const PHASE_S = Number(__ENV.PHASE_S || 30);
const KEYS = Number(__ENV.KEYS || 1000);

// DB-served reads per phase: repo_ms > 0 means the request missed the cache and
// hit Postgres (a cache hit returns before TrackRepo, so repo_ms stays 0). This
// is the T22a metric — proves whether an outage floods the DB.
const dbReads = new Counter("db_reads");

export const options = {
  scenarios: {
    read: {
      executor: "constant-arrival-rate",
      rate: RPS,
      timeUnit: "1s",
      duration: `${PHASE_S * 3}s`,
      preAllocatedVUs: VUS,
      maxVUs: VUS,
    },
  },
  // Per-phase sub-metrics surface the warm/outage/recovery breakdown directly in
  // the k6 summary. outage/recovery p95<50 FAIL on current single-tier code —
  // that failure is the point. fail-open must still hold (http_req_failed ~0).
  thresholds: {
    ...baseThresholds,
    "http_req_duration{phase:warm}": ["p(95)<50"],
    "http_req_duration{phase:outage}": ["p(95)<50"],
    "http_req_duration{phase:recovery}": ["p(95)<50"],
    // count>=0 always passes — its only job is to surface the per-phase DB-read
    // count in the summary (k6 shows a sub-metric only when a threshold names it).
    // The T22a "DB load stays flat" claim is read straight off these three.
    "db_reads{phase:warm}": ["count>=0"],
    "db_reads{phase:outage}": ["count>=0"],
    "db_reads{phase:recovery}": ["count>=0"],
  },
  summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
};

function currentPhase() {
  const elapsedS = exec.instance.currentTestRunDuration / 1000;
  if (elapsedS < PHASE_S) return "warm";
  if (elapsedS < PHASE_S * 2) return "outage";
  return "recovery";
}

export default function () {
  // ids 2..KEYS+1 — id 1 was soft-deleted in early testing; keep the working set
  // all-active so envelope checks stay green (the test measures cache, not 404s).
  const id = 2 + Math.floor(Math.random() * KEYS);
  const phase = currentPhase();
  const res = http.get(`${BASE}/items/${id}`, { tags: { phase } });
  envelopeOK(res);
  const timing = extractServerTiming(res);
  if (timing.repo > 0) dbReads.add(1, { phase });
}
