import http from "k6/http";
import exec from "k6/execution";
import { envelopeOK } from "./lib/checks.js";
import { baseThresholds } from "./lib/thresholds.js";

// Rung 5 proof — list-cache avalanche. Reproduces the synchronized-TTL expiry
// of cached list pages: warm PAGES distinct pages in a tight burst → all share
// one absolute expiry → at the TTL boundary every page misses → burst of
// count(*) seq scans + ListItems queries hit the DB simultaneously.
//
// MEASUREMENT: Postgres seq_scan delta on the items table (count(*) is a seq
// scan). The Makefile target polls pg_stat_user_tables per second — a spike at
// ~TTL = the avalanche.
//
// CURE: TTL jitter (CACHE_TTL_JITTER_PCT), not XFetch. Jitter spreads the single
// expiry of each page across a ±pct*TTL window at zero extra DB work; XFetch was
// tested here and rejected (delta << TTL → no-op at beta=1, more total work at
// high beta — see benchmark-logbook insight #18). Compare ±10% (spiky) vs ±50%
// (flat) to see the cure.
//
// Run the API with:
//   CACHE_ITEM_TTL_S=20  CACHE_L1_ENABLED=false  DB_MAX_CONNS=48
//   CACHE_TTL_JITTER_PCT=10   (before: spiky)   or   =50 (after: flat)
//
//   make load-cache-list-avalanche RPS=500 PAGES=500 ITEM_TTL_S=20

const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 500);
const VUS = Number(__ENV.VUS || 200);
const PAGES = Number(__ENV.PAGES || 10000);
const ITEM_TTL_S = Number(__ENV.ITEM_TTL_S || 20);
const GUARD_S = Number(__ENV.GUARD_S || 4);
const WARM_S = Number(__ENV.WARM_S || 10);
const SIZE = 20;

export const options = {
  scenarios: {
    warm: {
      executor: "shared-iterations",
      iterations: PAGES,
      vus: Math.min(VUS, PAGES),
      maxDuration: `${WARM_S}s`,
      startTime: "0s",
      exec: "warmPage",
    },
    read: {
      executor: "constant-arrival-rate",
      rate: RPS,
      timeUnit: "1s",
      startTime: `${WARM_S}s`,
      duration: `${ITEM_TTL_S * 2}s`,
      preAllocatedVUs: VUS,
      maxVUs: VUS,
      exec: "readPage",
    },
  },
  thresholds: {
    ...baseThresholds,
    "http_req_duration{phase:avalanche}": ["p(95)<200"],
  },
  summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
};

function randomPage() {
  return 1 + Math.floor(Math.random() * PAGES);
}

function currentPhase() {
  const elapsedS = exec.instance.currentTestRunDuration / 1000;
  if (elapsedS < ITEM_TTL_S - GUARD_S) return "pre";
  if (elapsedS <= ITEM_TTL_S + GUARD_S) return "avalanche";
  return "post";
}

export function warmPage() {
  const page = 1 + exec.scenario.iterationInInstance;
  http.get(`${BASE}/items?page=${page}&size=${SIZE}`, { tags: { phase: "warm" } });
}

export function readPage() {
  const res = http.get(`${BASE}/items?page=${randomPage()}&size=${SIZE}`, {
    tags: { phase: currentPhase() },
  });
  envelopeOK(res);
}
