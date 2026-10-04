import http from "k6/http";
import { envelopeOK } from "./lib/checks.js";
import { extractServerTiming } from "./lib/thresholds.js";

// Sudden burst 0 -> peak -> 0. Error rate must hold through the spike.
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 100);
const VUS = Number(__ENV.VUS || 50);
const JSON_HEADERS = { "Content-Type": "application/json" };

export const options = {
  scenarios: {
    spike: {
      executor: "ramping-arrival-rate",
      startRate: 0,
      timeUnit: "1s",
      preAllocatedVUs: VUS,
      maxVUs: VUS * 10,
      stages: [
        { target: 0, duration: "5s" },
        { target: RPS * 5, duration: "10s" }, // sudden burst
        { target: 0, duration: "5s" },
      ],
    },
  },
  thresholds: {
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
