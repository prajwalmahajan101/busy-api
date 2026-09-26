package store

import "testing"

func TestSafeOrderColumn(t *testing.T) {
	allowed := map[string]struct{}{"id": {}, "created_at": {}}

	cases := []struct {
		name string
		col  string
		want string
	}{
		{"whitelisted", "created_at", "created_at"},
		{"not whitelisted falls back to default", "id; DROP TABLE items", "id"},
		{"empty falls back to default", "", "id"},
		{"case-sensitive miss falls back", "ID", "id"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SafeOrderColumn(tc.col, allowed, "id"); got != tc.want {
				t.Fatalf("SafeOrderColumn(%q) = %q, want %q", tc.col, got, tc.want)
			}
		})
	}
}
