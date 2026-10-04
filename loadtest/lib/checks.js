import { check } from "k6";

const REQUEST_ID_RE = /^[A-Za-z0-9-]{1,128}$/;

// envelopeOK asserts the response body is the standard success envelope with a
// well-formed request_id. Returns the check boolean so callers can fail the iter.
export function envelopeOK(res) {
  let body = {};
  try {
    body = res.json();
  } catch (_) {
    body = {};
  }
  return check(res, {
    "envelope: success true": () => body.success === true,
    "envelope: has message": () =>
      typeof body.message === "string" && body.message.length > 0,
    "envelope: request_id well-formed": () =>
      typeof body.request_id === "string" &&
      REQUEST_ID_RE.test(body.request_id),
  });
}

// hasRequestIDHeader asserts the X-Request-ID header is present and matches the
// request_id echoed in the body.
export function hasRequestIDHeader(res) {
  const hdr = res.headers["X-Request-Id"] || res.headers["X-Request-ID"];
  let body = {};
  try {
    body = res.json();
  } catch (_) {
    body = {};
  }
  return check(res, {
    "header: X-Request-ID present": () => !!hdr && REQUEST_ID_RE.test(hdr),
    "header: matches body request_id": () =>
      !body.request_id || hdr === body.request_id,
  });
}

export function statusIs(res, want) {
  return check(res, { [`status is ${want}`]: (r) => r.status === want });
}
