import http from "k6/http";
import exec from "k6/execution";
import { envelopeOK } from "./lib/checks.js";
import { baseThresholds } from "./lib/thresholds.js";

// Rung 4c — T27 gate (XFetch, re-gated on DB read count).
//
// Measures the TTL-boundary DB-read spike that jitter (T24) + singleflight (T26)
// leave on the table — the residual that XFetch would fix.
//
// How it differs from the old cache_boundary.js:
//   OLD: single hot key + latency gate → singleflight collapses to 1 read, latency
//        flat, "not earned." Wrong gate — every 4c failure is a DB-work problem.
//   NEW: multi-key (like the avalanche scenario) + idx_scan poller (like the
//        stampede/penetration proofs). All shipping defences ON (jitter + singleflight),
//        L1 off (else L1 hides the L2 expiry from the DB).
//
// After T24 jitter, 1000 keys warmed in a burst expire across a ±jitter window
// (~4s at ±10% of 20s TTL). Singleflight collapses each key's concurrent misses to
// 1 DB read. Result: ~1000 DB reads in ~4-6s = a ~167-250/s spike over a 0/s
// baseline. This IS the failure XFetch addresses: it probabilistically spreads
// refreshes pre-expiry so they never cluster.
//
// MEASUREMENT: Postgres `pg_stat_user_tables.idx_scan` on `items` (same instrument
// as T25/T26/T28 — measure the DB, not the cache). Polled per second by the make
// target. Before the fix: a visible spike at the boundary. After (XFetch on): reads
// spread across the TTL window, spike flattens.
//
// Run the API with shipping defaults EXCEPT L1 off:
//   CACHE_ITEM_TTL_S=20 CACHE_TTL_JITTER_PCT=10 CACHE_L1_ENABLED=false DB_MAX_CONNS=1
//
//   make load-cache-boundary RPS=500 KEYS=1000 ITEM_TTL_S=20
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 500);
const VUS = Number(__ENV.VUS || 200);
const KEYS = Number(__ENV.KEYS || 1000);
const ITEM_TTL_S = Number(__ENV.ITEM_TTL_S || 20);
const WARM_S = Number(__ENV.WARM_S || 3);

export const options = {
  scenarios: {
    warm: {
      executor: "shared-iterations",
      iterations: KEYS,
      vus: Math.min(VUS, KEYS),
      maxDuration: `${WARM_S}s`,
      startTime: "0s",
      exec: "warmKey",
    },
    read: {
      executor: "constant-arrival-rate",
      rate: RPS,
      timeUnit: "1s",
      startTime: `${WARM_S}s`,
      duration: `${ITEM_TTL_S * 2}s`,
      preAllocatedVUs: VUS,
      maxVUs: VUS,
      exec: "readKey",
    },
  },
  thresholds: { ...baseThresholds },
  summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
};

function randomID() {
  return 2 + Math.floor(Math.random() * KEYS);
}

export function warmKey() {
  const id = 2 + exec.scenario.iterationInInstance;
  http.get(`${BASE}/items/${id}`, { tags: { phase: "warm" } });
}

export function readKey() {
  const res = http.get(`${BASE}/items/${randomID()}`, { tags: { phase: "read" } });
  envelopeOK(res);
}
