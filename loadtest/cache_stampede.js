import http from "k6/http";
import { envelopeOK } from "./lib/checks.js";
import { baseThresholds } from "./lib/thresholds.js";

// Rung 4c proof — cache stampede / dogpile (T25). Reproduces the duplicate DB
// reads that one HOT key's expiry triggers on the CURRENT code; the cure
// (collapse concurrent same-key misses via x/sync/singleflight) is T26.
//
// Distinct from the avalanche (T23/T24): jitter desyncs expiry ACROSS keys, but a
// single hot key still has ONE expiry instant. When it fires, every request
// in-flight for that key misses together and EACH runs its own DB read before the
// first one repopulates the cache — M duplicate reads for one logical value. An
// ideal cache does exactly 1 DB read per expiry (the rest wait for it). Run with
// jitter at its default: the stampede survives the T24 fix, proving it's separate.
//
// All load targets a single key (HOT_ID) at high concurrency so many requests
// overlap the empty-cache repopulation window. Short TTL so the run sees several
// expiry cycles; each cycle is a burst of M misses against a 0 baseline.
//
// MEASUREMENT: actual DB reads from Postgres `pg_stat_user_tables.idx_scan` on
// `items` (each GetItem is one PK index scan), polled per second by the make
// target. NOT Valkey `keyspace_misses` — that counts cache misses, and the fix
// (T26 singleflight) collapses the DB reads *downstream* of the misses while the
// miss count itself is unchanged (every request still does its own cache.Get).
// Measuring the cache would hide the fix entirely; measure the DB. With one key,
// idx_scan-per-cycle = the stampede factor M; ideal is 1/cycle.
//
// Run the API with the repro knobs (server-side; k6 cannot set them):
//   CACHE_ITEM_TTL_S=5      short TTL so several expiry cycles fit the run
//   CACHE_L1_ENABLED=false  else L1 serves the hot key and hides the L2 stampede
//   DB_MAX_CONNS=1          widens the repopulation window → the pileup is visible
//
//   make load-cache-stampede RPS=3000 VUS=400 HOT_ID=2 ITEM_TTL_S=5
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 3000);
const VUS = Number(__ENV.VUS || 400);
const DURATION_S = Number(__ENV.DURATION_S || 40);
const HOT_ID = Number(__ENV.HOT_ID || 2);

export const options = {
  scenarios: {
    hot: {
      executor: "constant-arrival-rate",
      rate: RPS,
      timeUnit: "1s",
      duration: `${DURATION_S}s`,
      preAllocatedVUs: VUS,
      maxVUs: VUS,
    },
  },
  thresholds: { ...baseThresholds },
  summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
};

export default function () {
  // Every request hammers the same hot key — the whole point of the proof.
  const res = http.get(`${BASE}/items/${HOT_ID}`);
  envelopeOK(res);
}
