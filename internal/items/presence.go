package items

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/bits-and-blooms/bloom/v3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Presence is an in-process bloom filter of existing item ids. It answers the
// one question that defeats cache penetration (T28): "could this id exist?" A
// flood of absent ids (the classic penetration DoS) is short-circuited to a 404
// in-process, before any cache or DB touch, so the DB read rate stays flat
// instead of tracking attacker RPS 1:1.
//
// The bloom never reports a false negative: an id that was Added always tests
// positive, so a real item is never wrongly 404'd. It may report a false
// positive (an absent id tests positive) — harmless, it just falls through to
// the DB, which answers correctly (and the negative cache absorbs repeats).
type Presence struct {
	mu     sync.RWMutex
	filter *bloom.BloomFilter
}

// idKey encodes an id into the fixed 8-byte key the bloom hashes.
func idKey(id int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(id))
	return b[:]
}

// NewPresence builds a bloom sized for capacity ids at the given false-positive
// rate, then seeds it from every existing id in the items table. 100k rows is a
// cheap one-shot stream at boot.
//
// ponytail: static capacity sizing. If the table outgrows capacity the FP rate
// climbs (more absent ids leak to the DB) but correctness holds — upgrade path is
// a periodic rebuild or a scalable bloom. Not built; revisit if the table 10×s.
func NewPresence(ctx context.Context, pool *pgxpool.Pool, capacity int, fpRate float64) (*Presence, error) {
	p := &Presence{filter: bloom.NewWithEstimates(uint(capacity), fpRate)}

	rows, err := pool.Query(ctx, "SELECT id FROM items")
	if err != nil {
		return nil, fmt.Errorf("presence: load ids: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("presence: scan id: %w", err)
		}
		p.filter.Add(idKey(id))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("presence: iterate ids: %w", err)
	}
	return p, nil
}

// MayExist reports whether id could be present. False means definitely absent
// (safe to 404 without touching the DB); true means maybe (check cache/DB).
//
// ponytail: single RWMutex, reads take RLock. Shard only if read contention
// shows up under profiling — the bloom Test is a handful of hash+bit-test ops.
func (p *Presence) MayExist(id int64) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.filter.Test(idKey(id))
}

// Add records a newly created id so subsequent reads for it pass the filter.
// Call it before returning the id to the caller: the caller only learns the id
// from our response, so no external Get can race ahead of the Add.
func (p *Presence) Add(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.filter.Add(idKey(id))
}
