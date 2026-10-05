import http from "k6/http";
import { envelopeOK } from "./lib/checks.js";
import { baseThresholds } from "./lib/thresholds.js";

// Rung 4c — T28a gate (hot-key protection). Hammers a single hot key at high
// RPS with L1 ON (shipping config) to test whether the hot key saturates Valkey.
//
// Distinct from the stampede proof (T25/T26): stampede deliberately disabled L1
// to isolate the L2 dogpile. This scenario keeps L1 ON — the real stack. If L1
// absorbs the hot key in-process (map lookup, no network), Valkey ops and DB reads
// should be ~0 after the initial fill.
//
// MEASUREMENT: two metrics polled per second by the make target:
//   1. Postgres idx_scan on items (DB reads) — same as T25/T26/T28.
//   2. Valkey keyspace_hits delta — every L2 read is a Valkey op. If L1 absorbs,
//      this stays ~0 (the hot key never reaches L2 after the first fill).
//
// Run the API with shipping defaults (L1 ON):
//   CACHE_L1_ENABLED=true CACHE_ITEM_TTL_S=300 DB_MAX_CONNS=1
//
//   make load-cache-hotkey RPS=3000 VUS=400 HOT_ID=2
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 3000);
const VUS = Number(__ENV.VUS || 400);
const DURATION_S = Number(__ENV.DURATION_S || 30);
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
  const res = http.get(`${BASE}/items/${HOT_ID}`);
  envelopeOK(res);
}
