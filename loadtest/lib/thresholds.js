import { Trend } from "k6/metrics";

// Shared pass/fail thresholds. baseThresholds is the global budget; rung1 is the
// tighter 1-rps baseline target (T14).
export const baseThresholds = {
  http_req_duration: ["p(95)<200"],
  http_req_failed: ["rate<0.01"],
  checks: ["rate==1.0"],
};

export const rung1Thresholds = {
  http_req_duration: ["p(95)<10"],
  http_req_failed: ["rate<0.01"],
  checks: ["rate==1.0"],
};

// Custom trends so per-layer timing shows up in the k6 summary.
export const serviceMS = new Trend("server_service_ms", true);
export const repoMS = new Trend("server_repo_ms", true);
export const handlerMS = new Trend("server_handler_ms", true);

// extractServerTiming parses the Server-Timing header into {handler, service, repo}
// (milliseconds) and records them into the trends. Returns the parsed object.
export function extractServerTiming(res) {
  const raw = res.headers["Server-Timing"] || "";
  const out = { handler: 0, service: 0, repo: 0 };
  for (const part of raw.split(",")) {
    const m = part.match(/(\w+);dur=(\d+(?:\.\d+)?)/);
    if (m) out[m[1]] = Number(m[2]);
  }
  handlerMS.add(out.handler);
  serviceMS.add(out.service);
  repoMS.add(out.repo);
  return out;
}
