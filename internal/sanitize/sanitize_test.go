package sanitize

import "testing"

func TestSanitize_MasksSensitiveKeys(t *testing.T) {
	in := map[string]any{
		"username":      "alice",
		"password":      "hunter2",
		"Authorization": "Bearer abc",
		"nested": map[string]any{
			"api_secret": "shh",
			"count":      3,
		},
	}
	got := Sanitize(in, 0).(map[string]any)
	if got["username"] != "alice" {
		t.Errorf("username masked: %v", got["username"])
	}
	if got["password"] != maskedValue {
		t.Errorf("password not masked: %v", got["password"])
	}
	if got["Authorization"] != maskedValue {
		t.Errorf("Authorization not masked: %v", got["Authorization"])
	}
	nested := got["nested"].(map[string]any)
	if nested["api_secret"] != maskedValue {
		t.Errorf("api_secret not masked: %v", nested["api_secret"])
	}
	if nested["count"] != 3 {
		t.Errorf("count altered: %v", nested["count"])
	}
}

func TestSanitize_TruncatesLongStrings(t *testing.T) {
	long := ""
	for range 100 {
		long += "x"
	}
	got := Sanitize(long, 10).(string)
	if got != "xxxxxxxxxx"+truncMarker {
		t.Errorf("truncate = %q", got)
	}
}

func TestSanitize_DoesNotMutateInput(t *testing.T) {
	in := map[string]any{"password": "secret"}
	_ = Sanitize(in, 0)
	if in["password"] != "secret" {
		t.Error("input was mutated")
	}
}

func TestSanitize_CapsSlices(t *testing.T) {
	big := make([]any, 150)
	for i := range big {
		big[i] = i
	}
	got := Sanitize(big, 0).([]any)
	if len(got) != maxSliceLen+1 { // +1 for the truncation marker
		t.Fatalf("len = %d, want %d", len(got), maxSliceLen+1)
	}
	if got[maxSliceLen] != truncMarker {
		t.Errorf("missing truncation marker: %v", got[maxSliceLen])
	}
}
