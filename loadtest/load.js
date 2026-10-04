import http from "k6/http";
import { envelopeOK } from "./lib/checks.js";
import { baseThresholds, extractServerTiming } from "./lib/thresholds.js";

// Ramp load to a target RPS. Read-heavy (the hot path the ladder tunes).
//   make load RPS=100 VUS=50 DURATION=1m
//   k6 run --out experimental-prometheus-rw loadtest/load.js -e RPS=100   # Grafana
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 100);
const VUS = Number(__ENV.VUS || 50);
const DURATION = __ENV.DURATION || "30s";
const JSON_HEADERS = { "Content-Type": "application/json" };

export const options = {
  scenarios: {
    ramp: {
      executor: "ramping-arrival-rate",
      startRate: 0,
      timeUnit: "1s",
      preAllocatedVUs: VUS,
      maxVUs: VUS * 4,
      stages: [
        { target: RPS, duration: "10s" },
        { target: RPS, duration: DURATION },
        { target: 0, duration: "5s" },
      ],
    },
  },
  thresholds: baseThresholds,
  // k6's default summary omits p99; list it explicitly so every rung row has it.
  summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
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
  let res = http.get(`${BASE}/items/${data.id}`);
  envelopeOK(res);
  extractServerTiming(res);

  res = http.get(`${BASE}/items?page=1&size=20`);
  envelopeOK(res);
  extractServerTiming(res);
}
