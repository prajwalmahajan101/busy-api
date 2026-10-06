package items

import (
	"testing"

	"github.com/bits-and-blooms/bloom/v3"
)

// newTestPresence builds a Presence without touching the DB (NewPresence seeds
// from Postgres; here we seed by hand to unit-test the filter behaviour).
func newTestPresence(capacity int, fpRate float64, ids ...int64) *Presence {
	p := &Presence{filter: bloom.NewWithEstimates(uint(capacity), fpRate)}
	for _, id := range ids {
		p.Add(id)
	}
	return p
}

// A bloom never reports a false negative: every id that was added MUST test
// positive, or a real item would be wrongly 404'd. This is the correctness
// invariant the whole T28 defence rests on.
func TestPresence_NoFalseNegatives(t *testing.T) {
	ids := make([]int64, 0, 10000)
	for i := int64(2); i < 10002; i++ {
		ids = append(ids, i)
	}
	p := newTestPresence(100000, 0.01, ids...)

	for _, id := range ids {
		if !p.MayExist(id) {
			t.Fatalf("false negative: added id %d tests absent — a real item would be 404'd", id)
		}
	}
}

// Absent ids should (mostly) test negative — that is the whole point, the DB is
// never touched for them. We allow the configured false-positive rate with slack
// and only assert the filter is doing real work, not that it is perfect.
func TestPresence_AbsentIDsMostlyNegative(t *testing.T) {
	present := make([]int64, 0, 1000)
	for i := int64(1); i <= 1000; i++ {
		present = append(present, i)
	}
	p := newTestPresence(10000, 0.01, present...)

	const n = 10000
	falsePos := 0
	for i := int64(1_000_000); i < 1_000_000+n; i++ { // disjoint from present
		if p.MayExist(i) {
			falsePos++
		}
	}
	// 1% target + generous slack for the probabilistic tail; a broken filter
	// (always-true) would land near 100%.
	if rate := float64(falsePos) / n; rate > 0.05 {
		t.Fatalf("false-positive rate %.3f too high — filter not short-circuiting absent ids", rate)
	}
}

// Add must be visible immediately (a just-created id is gettable). Guards the
// Create → Add → return ordering the service relies on.
func TestPresence_AddVisible(t *testing.T) {
	p := newTestPresence(1000, 0.01)
	if p.MayExist(42) {
		t.Fatal("id 42 present before Add (unexpected false positive in a fresh filter)")
	}
	p.Add(42)
	if !p.MayExist(42) {
		t.Fatal("id 42 absent after Add — new items would be 404'd")
	}
}
