import http from "k6/http";
import exec from "k6/execution";
import { envelopeOK } from "./lib/checks.js";
import { baseThresholds } from "./lib/thresholds.js";

// Rung 4c proof — cache avalanche (T23). Reproduces the synchronized-TTL expiry
// that stampedes the DB on the CURRENT (no-jitter) code; the cure (±jitter per
// Set) is T24.
//
// Root cause: every single-item read is cached with the SAME TTL
// (internal/items/service.go: s.cache.Set(..., s.cacheTTL)), and a cache HIT does
// not refresh it. Keys warmed in a burst therefore share one absolute expiry, so
// they all expire in the same instant and every subsequent read misses → DB herd.
// Random-arrival reads naturally desync expiry, so a steady run alone shows
// nothing — the repro must warm all keys in a tight burst first.
//
// Two scenarios share the global test clock:
//   warm — shared-iterations, one iteration per key (ids 2..KEYS+1), fires at
//          t=0 over <1s so every key's expiry lands near elapsed == ITEM_TTL_S.
//   read — constant-arrival-rate steady load from just after warm until
//          ~2*ITEM_TTL_S, so the synchronized miss actually gets exercised.
//
// MEASUREMENT: DB-read volume is captured OUTSIDE k6, from Valkey
// `keyspace_misses` (the `make load-cache-avalanche` target polls it per second).
// With L1 disabled every L2 miss is exactly one DB read, so a spike in the
// per-second miss delta at the TTL boundary — against a ~0 baseline — is the
// avalanche. We do NOT count misses via Server-Timing repo_ms: a single-row PK
// read is sub-millisecond and repo;dur rounds to 0, so that detector is blind
// here (it only worked in cache_resilience.js because the outage dial-tax made
// every read slow). dbsize collapsing from KEYS to a fraction in ~1s is the
// same event seen from the other side.
//
// Run the API with the repro knobs (k6 cannot set these — they are server-side):
//   CACHE_ITEM_TTL_S=20     short TTL so the avalanche lands inside the window
//   CACHE_L1_ENABLED=false  else L1's own TTL masks the L2 avalanche + miss count
//   DB_MAX_CONNS=1          (optional) matches the resilience-proof pool pinning
// and pass a matching ITEM_TTL_S to k6 so the latency phase boundary equals it:
//
//   make load-cache-avalanche RPS=500 KEYS=1000 ITEM_TTL_S=20
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 500);
const VUS = Number(__ENV.VUS || 200);
const KEYS = Number(__ENV.KEYS || 1000);
const ITEM_TTL_S = Number(__ENV.ITEM_TTL_S || 20);
const GUARD_S = Number(__ENV.GUARD_S || 4);
const WARM_S = Number(__ENV.WARM_S || 3); // headroom for the warm burst to finish

export const options = {
  scenarios: {
    // Warm every key once, at t=0, in a tight burst → shared expiry boundary.
    warm: {
      executor: "shared-iterations",
      iterations: KEYS,
      vus: Math.min(VUS, KEYS),
      maxDuration: `${WARM_S}s`,
      startTime: "0s",
      exec: "warmKey",
    },
    // Steady reads across the boundary so the synchronized miss is exercised.
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
  // Secondary latency tag. On fast hardware the herd inflates DB-read VOLUME,
  // not latency (a PK read is <1ms even under the burst), so this usually passes
  // — the volume spike in the poller table is the primary signal.
  thresholds: {
    ...baseThresholds,
    "http_req_duration{phase:avalanche}": ["p(95)<50"],
  },
  summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
};

function randomID() {
  // ids 2..KEYS+1 — id 1 was soft-deleted in early testing; keep the working set
  // all-active so envelope checks stay green.
  return 2 + Math.floor(Math.random() * KEYS);
}

// Keys are set by the warm burst at ~t=0, so the synchronized expiry lands at
// ~ITEM_TTL_S on the shared test clock (NOT WARM_S + ITEM_TTL_S — warm runs at
// startTime 0 and finishes in well under a second).
function currentPhase() {
  const elapsedS = exec.instance.currentTestRunDuration / 1000;
  if (elapsedS < ITEM_TTL_S - GUARD_S) return "pre";
  if (elapsedS <= ITEM_TTL_S + GUARD_S) return "avalanche";
  return "post";
}

export function warmKey() {
  const id = 2 + exec.scenario.iterationInInstance; // one unique key per iteration
  http.get(`${BASE}/items/${id}`, { tags: { phase: "warm" } });
}

export function readKey() {
  const res = http.get(`${BASE}/items/${randomID()}`, {
    tags: { phase: currentPhase() },
  });
  envelopeOK(res);
}
