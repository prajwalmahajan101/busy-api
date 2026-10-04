import http from "k6/http";
import { envelopeOK } from "./lib/checks.js";
import { extractServerTiming } from "./lib/thresholds.js";

// 30-minute sustained load. Watch goroutines/heap/dropped_total server-side
// (via logs now; via OTel metrics once rung 5 lands) for leaks — they stay flat.
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 50);
const VUS = Number(__ENV.VUS || 50);
const JSON_HEADERS = { "Content-Type": "application/json" };

export const options = {
  scenarios: {
    soak: {
      executor: "constant-arrival-rate",
      rate: RPS,
      timeUnit: "1s",
      duration: "30m",
      preAllocatedVUs: VUS,
      maxVUs: VUS * 4,
    },
  },
  thresholds: {
    http_req_duration: ["p(95)<200"],
    http_req_failed: ["rate<0.01"],
  },
};

export function setup() {
  const res = http.post(
    `${BASE}/items`,
    JSON.stringify({ notes: { seed: true } }),
    {
      headers: JSON_HEADERS,
    },
  );
  return { id: res.json().data.id };
}

export default function (data) {
  const res = http.get(`${BASE}/items/${data.id}`);
  envelopeOK(res);
  extractServerTiming(res);
}
