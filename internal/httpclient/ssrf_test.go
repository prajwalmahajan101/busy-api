package httpclient

import (
	"net/netip"
	"testing"
)

func TestBlockedIP(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":            true,
		"10.0.0.5":             true,
		"192.168.1.1":          true,
		"172.16.0.1":           true,
		"169.254.169.254":      true, // cloud metadata
		"0.0.0.0":              true,
		"::1":                  true,
		"fc00::1":              true,
		"fe80::1":              true,
		"1.1.1.1":              false,
		"8.8.8.8":              false,
		"2606:4700:4700::1111": false,
	}
	for s, want := range cases {
		if got := blockedIP(netip.MustParseAddr(s)); got != want {
			t.Errorf("blockedIP(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestAssertPublicURL(t *testing.T) {
	if err := AssertPublicURL("https://example.com/path", false); err != nil {
		t.Errorf("public url rejected: %v", err)
	}
	for _, raw := range []string{"http://127.0.0.1", "http://10.0.0.1/x", "https://[::1]:8443"} {
		if err := AssertPublicURL(raw, false); err == nil {
			t.Errorf("expected rejection for %s", raw)
		}
	}
	if err := AssertPublicURL("http://127.0.0.1", true); err != nil {
		t.Errorf("allowPrivate should permit loopback: %v", err)
	}
	if err := AssertPublicURL("ftp://example.com", false); err == nil {
		t.Error("expected scheme rejection")
	}
}

func TestSafeHost(t *testing.T) {
	if got := SafeHost("https://user:pass@example.com:8443/x?q=1"); got != "example.com" {
		t.Errorf("SafeHost = %q, want example.com", got)
	}
	if got := SafeHost("://bad"); got != "invalid" {
		t.Errorf("SafeHost(bad) = %q, want invalid", got)
	}
}
