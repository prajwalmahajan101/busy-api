import http from "k6/http";
import { envelopeOK } from "./lib/checks.js";
import { baseThresholds, extractServerTiming } from "./lib/thresholds.js";

// Rung 4 cache-aside proof. Hammers the cacheable hot read GET /items/:id over a
// bounded working set of KEYS ids so the cache gets hits. Run twice against a
// pool-constrained server (DB_MAX_CONNS=1, simulating DB saturation at scale):
//   * Valkey DOWN  -> every cache op fails open to a miss -> all reads hit the
//     one DB connection -> p95 blows past the 50ms budget (the failure).
//   * Valkey UP    -> hits bypass the pool entirely -> p95 back under budget.
//
//   make load-cache RPS=2000 VUS=300          (Valkey up = pass)
//   (stop valkey, rerun = fail / proves fail-open: still 0 5xx)
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 2000);
const VUS = Number(__ENV.VUS || 300);
const DURATION = __ENV.DURATION || "30s";
const KEYS = Number(__ENV.KEYS || 1000);

export const options = {
  scenarios: {
    read: {
      executor: "ramping-arrival-rate",
      startRate: 0,
      timeUnit: "1s",
      preAllocatedVUs: VUS,
      maxVUs: VUS,
      stages: [
        { target: RPS, duration: "5s" },
        { target: RPS, duration: DURATION },
        { target: 0, duration: "5s" },
      ],
    },
  },
  thresholds: baseThresholds,
  summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
};

export default function () {
  // ids 2..KEYS+1 — id 1 was soft-deleted in early testing; keep the working set
  // all-active so envelope checks stay green (the test measures cache, not 404s).
  const id = 2 + Math.floor(Math.random() * KEYS);
  const res = http.get(`${BASE}/items/${id}`);
  envelopeOK(res);
  extractServerTiming(res);
}
