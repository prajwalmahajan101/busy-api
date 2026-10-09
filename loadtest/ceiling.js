import http from "k6/http";
import { check } from "k6";

// Throughput-ceiling profile. Same 50/50 item+list read mix as load.js, but the
// load generator is stripped to the bone so ONE k6 box can push far more rps:
//   - discardResponseBodies: true  → k6 never allocates/parses the response body
//     (res.json() per response was what pegged the k6 box CPU at ~21.7K rps).
//   - status-only check             → no JSON envelope parse per response.
// Use this to find the SERVER's ceiling once load.js has shown the server is no
// longer the bottleneck (handler_ms ~0, server box half-idle, k6 box saturated).
//
//   make load-ceiling BASE_URL=http://<api-private-ip>:8000 RPS=30000 VUS=6000 DURATION=30s
const BASE = __ENV.BASE_URL || "http://localhost:8000";
const RPS = Number(__ENV.RPS || 30000);
const VUS = Number(__ENV.VUS || 6000);
const DURATION = __ENV.DURATION || "30s";
const JSON_HEADERS = { "Content-Type": "application/json" };

export const options = {
  discardResponseBodies: true, // the single biggest k6-side CPU saving
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
  thresholds: {
    http_req_duration: ["p(95)<200"],
    http_req_failed: ["rate<0.01"],
  },
  summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
};

// setup needs the created id, so it overrides the global discard for this one
// request (responseType: "text") to read the body.
export function setup() {
  const res = http.post(
    `${BASE}/items`,
    JSON.stringify({ notes: { seed: true } }),
    { headers: JSON_HEADERS, responseType: "text" },
  );
  return { id: JSON.parse(res.body).data.id };
}

export default function (data) {
  const item = http.get(`${BASE}/items/${data.id}`);
  check(item, { "item 200": (r) => r.status === 200 });

  const list = http.get(`${BASE}/items?page=1&size=20`);
  check(list, { "list 200": (r) => r.status === 200 });
}
