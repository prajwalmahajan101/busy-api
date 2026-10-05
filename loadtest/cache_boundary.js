import http from "k6/http";
import exec from "k6/execution";
import { envelopeOK } from "./lib/checks.js";
import { baseThresholds } from "./lib/thresholds.js";

// Rung 4c — T27 go/no-go (XFetch). XFetch (probabilistic early recompute) only
// earns its place IF, after singleflight (T26), p99 still SPIKES at the TTL
// boundary — i.e. the one leader + the followers waiting on it in singleflight.Do
// pay a visible latency penalty when a hot key expires. If the boundary p99 is
// flat vs steady-state, XFetch is not earned (enforcement rule) and T27 is skipped.
//
// Single hot key, jitter OFF (CACHE_TTL_JITTER_PCT=0 so expiry lands on a clean
// TTL cadence), DB_MAX_CONNS=1 (worst case — the leader's cold read is the slowest
// it can be). Each request is tagged `boundary` when it lands within ±GUARD_S of
// an expiry instant (elapsed mod TTL), else `steady`. Compare p99{boundary} vs
// p99{steady}: a spike at the boundary = XFetch earned; flat = skip T27.
//
//   CACHE_ITEM_TTL_S=10 CACHE_TTL_JITTER_PCT=0 CACHE_L1_ENABLED=false DB_MAX_CONNS=1 ./bin/server
//   k6 run loadtest/cache_boundary.js -e RPS=3000 -e VUS=400 -e ITEM_TTL_S=10
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 3000);
const VUS = Number(__ENV.VUS || 400);
const DURATION_S = Number(__ENV.DURATION_S || 60);
const HOT_ID = Number(__ENV.HOT_ID || 2);
const ITEM_TTL_S = Number(__ENV.ITEM_TTL_S || 10);
const GUARD_S = Number(__ENV.GUARD_S || 0.7);

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
  // The whole question: is p99 at the expiry boundary worse than steady-state?
  thresholds: {
    ...baseThresholds,
    "http_req_duration{phase:boundary}": ["p(99)<50"],
    "http_req_duration{phase:steady}": ["p(99)<50"],
  },
  summaryTrendStats: ["avg", "med", "p(90)", "p(95)", "p(99)", "p(99.9)", "max"],
};

// The hot key is (re)cached at ~0 and every ITEM_TTL_S after, so an expiry instant
// sits at each multiple of ITEM_TTL_S on the shared clock. A request is "boundary"
// if it lands within ±GUARD_S of one (where a cold recompute + follower-wait would
// show up), else "steady".
function phase() {
  const elapsed = exec.instance.currentTestRunDuration / 1000;
  // Skip the first full cycle: t=0 is k6 ramp-up + cold connections + the first
  // cache fill, NOT a TTL expiry — tagging it "boundary" (elapsed%TTL≈0) poisons
  // the bucket with startup latency. Real expiries land at every ITEM_TTL_S after.
  if (elapsed < ITEM_TTL_S) return "warmup";
  const m = elapsed % ITEM_TTL_S;
  return m <= GUARD_S || m >= ITEM_TTL_S - GUARD_S ? "boundary" : "steady";
}

export default function () {
  const res = http.get(`${BASE}/items/${HOT_ID}`, { tags: { phase: phase() } });
  envelopeOK(res);
}
