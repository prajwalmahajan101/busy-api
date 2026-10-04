import http from "k6/http";
import { textSummary } from "https://jslib.k6.io/k6-summary/0.0.2/index.js";
import { envelopeOK } from "./lib/checks.js";
import { extractServerTiming } from "./lib/thresholds.js";

// Ramp PAST the target RPS until thresholds break; abort at the break point.
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 100);
const VUS = Number(__ENV.VUS || 50);
const JSON_HEADERS = { "Content-Type": "application/json" };

export const options = {
  scenarios: {
    stress: {
      executor: "ramping-arrival-rate",
      startRate: RPS,
      timeUnit: "1s",
      preAllocatedVUs: VUS,
      maxVUs: VUS * 10,
      stages: [
        { target: RPS, duration: "20s" },
        { target: RPS * 2, duration: "20s" },
        { target: RPS * 5, duration: "20s" },
        { target: RPS * 10, duration: "20s" },
      ],
    },
  },
  thresholds: {
    http_req_duration: [{ threshold: "p(95)<200", abortOnFail: true }],
    http_req_failed: [{ threshold: "rate<0.01", abortOnFail: true }],
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

// Break-point numbers for the logbook.
export function handleSummary(data) {
  return {
    stdout: textSummary(data, { indent: " ", enableColors: true }),
    "stress-summary.json": JSON.stringify(data, null, 2),
  };
}
