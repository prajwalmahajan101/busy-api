import http from "k6/http";
import { envelopeOK, hasRequestIDHeader, statusIs } from "./lib/checks.js";
import { baseThresholds, extractServerTiming } from "./lib/thresholds.js";

const BASE = __ENV.BASE_URL || "http://localhost:8000";
const JSON_HEADERS = { "Content-Type": "application/json" };

export const options = {
  vus: 1,
  duration: "30s",
  thresholds: baseThresholds,
};

// setup creates one item so the read routes have a known id.
export function setup() {
  const res = http.post(
    `${BASE}/items`,
    JSON.stringify({ notes: { seed: true } }),
    {
      headers: JSON_HEADERS,
    },
  );
  statusIs(res, 201);
  return { id: res.json().data.id };
}

export default function (data) {
  // GET /ping
  let res = http.get(`${BASE}/ping`);
  statusIs(res, 200);
  hasRequestIDHeader(res);
  extractServerTiming(res);

  // POST /items
  res = http.post(`${BASE}/items`, JSON.stringify({ notes: { smoke: true } }), {
    headers: JSON_HEADERS,
  });
  statusIs(res, 201);
  envelopeOK(res);
  hasRequestIDHeader(res);
  extractServerTiming(res);

  // GET /items (list)
  res = http.get(`${BASE}/items?page=1&size=20`);
  statusIs(res, 200);
  envelopeOK(res);
  extractServerTiming(res);

  // GET /items/:id
  res = http.get(`${BASE}/items/${data.id}`);
  statusIs(res, 200);
  envelopeOK(res);
  hasRequestIDHeader(res);
  extractServerTiming(res);
}
