import http from "k6/http";
import { statusIs } from "./lib/checks.js";

// Rung 4c proof — cache penetration (T28). Reproduces the DB load a flood of
// requests for NON-EXISTENT ids inflicts on the CURRENT code; the cure (bloom
// pre-filter + negative cache) is the rest of T28.
//
// Distinct from avalanche (T23/T24) and stampede (T25/T26): those protect keys
// that DO resolve to a row. Penetration is the opposite — every id is absent, so
// nothing ever caches a hit. Each request misses the cache and falls straight
// through to GetItem → pgx.ErrNoRows → one PK index scan. There is no hot key and
// no shared expiry: singleflight can't collapse distinct keys, L1/L2 never hold a
// value. Result: DB read rate tracks request rate 1:1 — a trivial DoS vector.
//
// Every request uses a fresh random id in a guaranteed-absent range (the 100k
// seed is ids 1..100000, so 100_000_000 + rand never exists). Unique ids defeat
// any same-key collapse, which is the point.
//
// MEASUREMENT: actual DB reads from Postgres `pg_stat_user_tables.idx_scan` on
// `items` (each GetItem is one PK index scan, even when it returns 0 rows), polled
// per second by the make target. Same technique as the stampede proof (T25/T26):
// measure the DB, not the cache. Before the fix, idx_scan delta ≈ RPS. After, ≈ 0
// (the bloom short-circuits before any DB touch).
//
// Run the API with the repro knobs (server-side; k6 cannot set them):
//   CACHE_BLOOM_ENABLED=false  the fix under test — off to see the symptom
//   CACHE_L1_ENABLED=false     keep the tiers out of the picture
//   DB_MAX_CONNS=1             surface pool contention + tail latency under the flood
//
//   make load-cache-penetration RPS=3000 VUS=400
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 3000);
const VUS = Number(__ENV.VUS || 400);
const DURATION_S = Number(__ENV.DURATION_S || 40);
// Base of the absent id range. Default sits far above the 100k seed so no id exists.
const ABSENT_BASE = Number(__ENV.ABSENT_BASE || 100000000);
const ABSENT_SPAN = Number(__ENV.ABSENT_SPAN || 10000000);

export const options = {
  scenarios: {
    flood: {
      executor: "constant-arrival-rate",
      rate: RPS,
      timeUnit: "1s",
      duration: `${DURATION_S}s`,
      preAllocatedVUs: VUS,
      maxVUs: VUS,
    },
  },
  // NB: no http_req_failed threshold — every response is a 404 by design, so the
  // standard <1% budget would abort the run. We assert the 404 explicitly instead.
  thresholds: {
    http_req_duration: ["p(95)<200"],
    checks: ["rate==1.0"],
  },
  summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
};

export default function () {
  const id = ABSENT_BASE + Math.floor(Math.random() * ABSENT_SPAN);
  const res = http.get(`${BASE}/items/${id}`);
  statusIs(res, 404);
}
