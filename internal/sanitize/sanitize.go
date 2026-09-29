// Package sanitize masks sensitive values and truncates oversized ones before
// they reach logs or the audit trail. Stdlib-only; reused by internal/httpclient
// (outbound body logging) and the Phase 7 apilog body capture.
package sanitize

import (
	"regexp"
	"unicode/utf8"
)

// sensitiveKey matches map keys whose values must never be logged in clear.
var sensitiveKey = regexp.MustCompile(`(?i)password|token|secret|key|authorization`)

// maskedValue replaces any sensitive value.
const maskedValue = "***"

// maxSliceLen bounds how many elements of a slice are kept, so a huge list can
// never blow up a log line.
const maxSliceLen = 100

// truncMarker is appended when a string or slice is cut.
const truncMarker = "…(truncated)"

// Sanitize returns a copy of v safe to log: values under sensitive keys are
// masked, strings longer than maxLen are truncated, and long slices are capped.
// maxLen <= 0 disables string truncation. The input is never mutated.
func Sanitize(v any, maxLen int) any {
	return sanitize(v, maxLen, false)
}

// sanitize walks v recursively. masked=true forces the whole subtree to the mask
// (a sensitive key hides its entire value, however nested).
func sanitize(v any, maxLen int, masked bool) any {
	if masked {
		return maskedValue
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = sanitize(val, maxLen, sensitiveKey.MatchString(k))
		}
		return out
	case []any:
		n := len(t)
		capped := false
		if n > maxSliceLen {
			n = maxSliceLen
			capped = true
		}
		out := make([]any, 0, n+1)
		for i := 0; i < n; i++ {
			out = append(out, sanitize(t[i], maxLen, false))
		}
		if capped {
			out = append(out, truncMarker)
		}
		return out
	case string:
		return truncate(t, maxLen)
	case []byte:
		return truncate(string(t), maxLen)
	default:
		return v
	}
}

// truncate cuts s to at most maxLen bytes without splitting a UTF-8 rune.
func truncate(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	cut := maxLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncMarker
}
