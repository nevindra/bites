package bites

import (
	"context"
	"time"
)

// RaceEnabled lets the external tests skip an allocation count the race detector changes.
const RaceEnabled = raceEnabled

// StartForTest freezes the app and builds what start-up builds (the API document), without serving.
func StartForTest(a *App) { a.start() }

// MemoryRateLimitStore is the in-memory buckets of a RateLimiter behind the RateLimitStore
// interface, so the conformance suite of bitetest holds the store a limiter uses by default to the
// contract it states for every store.
func MemoryRateLimitStore() RateLimitStore { return memoryStore{newMemoryBuckets(defaultMaxKeys)} }

type memoryStore struct{ b *memoryBuckets }

func (m memoryStore) Take(_ context.Context, key string, now time.Time, burst int, interval time.Duration) (RateLimitDecision, error) {
	return m.b.take([]byte(key), now, burst, interval), nil
}
