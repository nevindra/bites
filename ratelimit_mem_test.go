package bites

import (
	"hash/maphash"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// The in-memory store's bound under a client that rotates keys (ADR 0055).

func TestAClientRotatingKeysNeverGrowsTheMemoryStorePastMaxKeys(t *testing.T) {
	b := newMemoryBuckets(512)
	now := time.Unix(1_700_000_000, 0)
	allowed := 0
	for i := range 100_000 {
		if b.take([]byte("k"+strconv.Itoa(i)), now, 5, time.Second).Allowed {
			allowed++
		}
	}
	total := 0
	for i := range b.shards {
		n := len(b.shards[i].m)
		if n > b.cap {
			t.Fatalf("shard %d holds %d keys, over its %d", i, n, b.cap)
		}
		total += n
	}
	t.Logf("100,000 keys rotated through a store of 512: %d held, %d allowed", total, allowed)
	if total > 512 || allowed != 100_000 {
		t.Fatalf("held %d keys, allowed %d", total, allowed)
	}
}

func TestEvictionDropsTheKeysWhoseBucketIsFullBeforeAKeyThatIsStillLimited(t *testing.T) {
	b := newMemoryBuckets(8 * memShards) // 8 keys a shard
	shardOf := func(k string) uint64 { return maphash.Bytes(b.seed, []byte(k)) % memShards }
	hot := "hot"
	var same []string
	for i := 0; len(same) < 8; i++ {
		if k := "k" + strconv.Itoa(i); shardOf(k) == shardOf(hot) {
			same = append(same, k)
		}
	}
	t0 := time.Unix(1_700_000_000, 0)
	for range 10 {
		b.take([]byte(hot), t0, 10, time.Second)
	}
	for _, k := range same[:7] {
		b.take([]byte(k), t0, 10, time.Second)
	}
	if d := b.take([]byte(hot), t0, 10, time.Second); d.Allowed {
		t.Fatal("the hot key was not limited")
	}
	now := t0.Add(2 * time.Second) // the seven have refilled; the hot key has not
	if !b.take([]byte(same[7]), now, 10, time.Second).Allowed {
		t.Fatal("a new key was refused")
	}
	// Two seconds earned the hot key two requests back; a key evicted and made afresh would have nine left.
	if d := b.take([]byte(hot), now, 10, time.Second); !d.Allowed || d.Remaining != 1 {
		t.Fatalf("the hot key lost its state to eviction: %+v", d)
	}
	if n := len(b.shards[shardOf(hot)].m); n != 2 {
		t.Fatalf("the shard holds %d keys, want the hot key and the new one", n)
	}
}

// TestTheMemoryStoreHoldsAKeyInAboutFiftyBytes is the measurement behind MaxKeys's "about 5 MB":
// the heap after a collection, before and after 100,000 keys (49 B a key for IPv4 and 50 B for
// IPv6 when it was written; the ceiling leaves room for a runtime's map layout to change).
func TestTheMemoryStoreHoldsAKeyInAboutFiftyBytes(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes the heap")
	}
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	b := newMemoryBuckets(100_000)
	now := time.Unix(1_700_000_000, 0)
	for i := range 100_000 {
		b.take([]byte{0x20, 1, 0xd, 0xb8, 0, byte(i >> 16), byte(i >> 8), byte(i)}, now, 5, time.Second)
	}
	runtime.GC()
	runtime.ReadMemStats(&m1)
	per := int64(m1.HeapAlloc-m0.HeapAlloc) / 100_000
	runtime.KeepAlive(b)
	t.Logf("the in-memory store holds an IPv6 key in %d B", per)
	if per > 64 {
		t.Fatalf("%d B a key, over the ceiling of 64", per)
	}
}
