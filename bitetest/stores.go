package bitetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/bites"
)

// This file holds the conformance suites of the interfaces a module or an application implements:
// bites.RateLimitStore, bites.SessionStore, bites.OwnerSessionStore and bites.Broker. Each suite
// is a function that takes a *testing.T and a factory, and runs one subtest for each promise the
// doc comment of its interface makes. A store written outside the core calls the suite from its own
// test, so a promise it breaks fails with the sentence of the promise:
//
//	func TestMyStoreKeepsTheContract(t *testing.T) {
//		bitetest.TestRateLimitStore(t, func(t *testing.T) bites.RateLimitStore { return newStore(t) })
//	}

// TestRateLimitStore checks a bites.RateLimitStore against the contract of its doc comment, one
// subtest a promise: a bucket of burst requests that earns one back every interval, a refused
// request that spends nothing, Remaining, RetryAfter and Reset as the interface defines them, keys
// that never share a bucket, and Take atomic for a key under concurrent callers. newStore is
// called once for each subtest and must return a store with no state of any earlier one (a new
// table, a new key prefix), so the subtests cannot hear each other; it may call t.Cleanup. The
// suite gives Take its own times, a fixed base moved forward by the test, and never sleeps, so a
// store must decide with the now it is given and not with a clock of its own. A duration is
// compared to within a millisecond, which a store that keeps milliseconds meets. A nil newStore
// fails the test.
func TestRateLimitStore(t *testing.T, newStore func(t *testing.T) bites.RateLimitStore) {
	t.Helper()
	if newStore == nil {
		t.Fatal("bitetest.TestRateLimitStore: newStore is nil: give a function that returns a new bites.RateLimitStore for each subtest")
	}
	const sec = time.Second
	base := time.Unix(1_700_000_000, 0)
	ctx := context.Background()
	take := func(t *testing.T, s bites.RateLimitStore, key string, now time.Time, burst int, interval time.Duration) bites.RateLimitDecision {
		t.Helper()
		d, err := s.Take(ctx, key, now, burst, interval)
		if err != nil {
			t.Fatalf("Take(%q) returned the error %v", key, err)
		}
		return d
	}
	near := func(a, b time.Duration) bool { d := a - b; return d > -time.Millisecond && d < time.Millisecond }
	run := func(name string, f func(t *testing.T, s bites.RateLimitStore)) {
		t.Run(name, func(t *testing.T) { f(t, newStore(t)) })
	}
	exhaust := func(t *testing.T, s bites.RateLimitStore, key string, now time.Time, burst int, interval time.Duration) {
		t.Helper()
		for i := range burst {
			if d := take(t, s, key, now, burst, interval); !d.Allowed {
				t.Fatalf("request %d of a burst of %d was refused: %+v", i+1, burst, d)
			}
		}
	}

	run("a new key may make burst requests at once and Remaining counts down to zero", func(t *testing.T, s bites.RateLimitStore) {
		for i := range 3 {
			d := take(t, s, "k", base, 3, sec)
			if !d.Allowed || d.Remaining != 2-i || d.RetryAfter != 0 || !near(d.Reset, time.Duration(i+1)*sec) {
				t.Fatalf("request %d: %+v, want Allowed, Remaining %d, RetryAfter 0, Reset %v", i+1, d, 2-i, time.Duration(i+1)*sec)
			}
		}
	})
	run("a request over the burst is refused with Remaining 0, RetryAfter of one interval and Reset of the whole bucket", func(t *testing.T, s bites.RateLimitStore) {
		exhaust(t, s, "k", base, 3, sec)
		d := take(t, s, "k", base, 3, sec)
		if d.Allowed || d.Remaining != 0 || !near(d.RetryAfter, sec) || !near(d.Reset, 3*sec) {
			t.Fatalf("the fourth request: %+v, want refused, Remaining 0, RetryAfter 1s, Reset 3s", d)
		}
	})
	run("a burst of one allows one request and refuses the next for a whole interval", func(t *testing.T, s bites.RateLimitStore) {
		d := take(t, s, "k", base, 1, 10*sec)
		if !d.Allowed || d.Remaining != 0 || !near(d.Reset, 10*sec) {
			t.Fatalf("the first request: %+v", d)
		}
		d = take(t, s, "k", base, 1, 10*sec)
		if d.Allowed || !near(d.RetryAfter, 10*sec) {
			t.Fatalf("the second request: %+v, want refused with RetryAfter 10s", d)
		}
	})
	run("RetryAfter is when the next request is allowed, not before", func(t *testing.T, s bites.RateLimitStore) {
		exhaust(t, s, "k", base, 2, 4*sec)
		d := take(t, s, "k", base, 2, 4*sec)
		if d.Allowed || d.RetryAfter <= 0 {
			t.Fatalf("a refused request: %+v", d)
		}
		half := base.Add(d.RetryAfter / 2)
		if d := take(t, s, "k", half, 2, 4*sec); d.Allowed {
			t.Fatalf("a request half of RetryAfter later was allowed: %+v", d)
		}
		at := base.Add(d.RetryAfter + time.Millisecond)
		if d := take(t, s, "k", at, 2, 4*sec); !d.Allowed {
			t.Fatalf("a request RetryAfter later was refused: %+v", d)
		}
		if d := take(t, s, "k", at, 2, 4*sec); d.Allowed {
			t.Fatalf("a second request at the same time was allowed: %+v", d)
		}
	})
	run("a refused request spends nothing", func(t *testing.T, s bites.RateLimitStore) {
		exhaust(t, s, "k", base, 2, sec)
		for range 20 {
			if d := take(t, s, "k", base, 2, sec); d.Allowed {
				t.Fatalf("a request over the burst was allowed: %+v", d)
			}
		}
		at := base.Add(sec + time.Millisecond)
		if d := take(t, s, "k", at, 2, sec); !d.Allowed {
			t.Fatalf("one interval after the burst, the request was refused: refused requests were charged: %+v", d)
		}
		if d := take(t, s, "k", at, 2, sec); d.Allowed {
			t.Fatalf("two requests were allowed after one interval: %+v", d)
		}
	})
	run("a key earns one request back every interval", func(t *testing.T, s bites.RateLimitStore) {
		exhaust(t, s, "k", base, 4, sec)
		d := take(t, s, "k", base.Add(2*sec), 4, sec)
		if !d.Allowed || d.Remaining != 1 {
			t.Fatalf("after two intervals: %+v, want Allowed with Remaining 1 (two earned, one spent)", d)
		}
		if d := take(t, s, "k", base.Add(2*sec), 4, sec); !d.Allowed || d.Remaining != 0 {
			t.Fatalf("the second request after two intervals: %+v, want Allowed with Remaining 0", d)
		}
		if d := take(t, s, "k", base.Add(2*sec), 4, sec); d.Allowed {
			t.Fatalf("a third request after two intervals was allowed: %+v", d)
		}
	})
	run("a bucket idle for longer than it takes to fill is full again and never holds more than the burst", func(t *testing.T, s bites.RateLimitStore) {
		exhaust(t, s, "k", base, 3, sec)
		later := base.Add(1000 * sec)
		for i := range 3 {
			d := take(t, s, "k", later, 3, sec)
			if !d.Allowed || d.Remaining != 2-i {
				t.Fatalf("request %d after a long idle: %+v, want Allowed with Remaining %d", i+1, d, 2-i)
			}
		}
		if d := take(t, s, "k", later, 3, sec); d.Allowed {
			t.Fatalf("a fourth request after a long idle was allowed: %+v", d)
		}
	})
	run("Reset is how long until the bucket is full again", func(t *testing.T, s bites.RateLimitStore) {
		d := take(t, s, "k", base, 5, 2*sec)
		if !near(d.Reset, 2*sec) {
			t.Fatalf("after one request, Reset %v, want 2s", d.Reset)
		}
		d = take(t, s, "k", base.Add(sec), 5, 2*sec)
		if !near(d.Reset, 3*sec) {
			t.Fatalf("a second request a second later, Reset %v, want 3s (4s of tokens spent, 1s earned)", d.Reset)
		}
		d = take(t, s, "k", base.Add(3600*sec), 5, 2*sec)
		if !near(d.Reset, 2*sec) {
			t.Fatalf("a request an hour later, Reset %v, want 2s", d.Reset)
		}
	})
	run("a now earlier than one already seen gives nothing back", func(t *testing.T, s bites.RateLimitStore) {
		if d := take(t, s, "k", base.Add(100*sec), 1, sec); !d.Allowed {
			t.Fatalf("the first request: %+v", d)
		}
		if d := take(t, s, "k", base.Add(50*sec), 1, sec); d.Allowed {
			t.Fatalf("a request at an earlier now was allowed: %+v", d)
		}
	})
	run("keys do not share a bucket", func(t *testing.T, s bites.RateLimitStore) {
		exhaust(t, s, "alice@example.com", base, 2, sec)
		if d := take(t, s, "alice@example.com", base, 2, sec); d.Allowed {
			t.Fatalf("the exhausted key allowed a request: %+v", d)
		}
		for _, k := range []string{"bob@example.com", "Alice@example.com", "alice@example.com ", "", "203.0.113.7", "2001:db8::/64", "alice@example.com\x00", "\xff\xfe", "café", strings.Repeat("k", 4096)} {
			d := take(t, s, k, base, 2, sec)
			if !d.Allowed || d.Remaining != 1 {
				t.Fatalf("a key that was never used, %q: %+v, want a full bucket", k, d)
			}
		}
	})
	run("the empty key is a bucket like any other", func(t *testing.T, s bites.RateLimitStore) {
		exhaust(t, s, "", base, 2, sec)
		if d := take(t, s, "", base, 2, sec); d.Allowed {
			t.Fatalf("the empty key allowed a request over its burst: %+v", d)
		}
	})
	run("a bucket of a thousand requests an hour is carried with its long durations", func(t *testing.T, s bites.RateLimitStore) {
		exhaust(t, s, "k", base, 1000, time.Hour)
		d := take(t, s, "k", base, 1000, time.Hour)
		if d.Allowed || !near(d.RetryAfter, time.Hour) || !near(d.Reset, 1000*time.Hour) {
			t.Fatalf("a bucket of 1000 an hour: %+v, want refused, RetryAfter 1h, Reset 1000h", d)
		}
	})
	run("two callers never both get the last request of a key", func(t *testing.T, s bites.RateLimitStore) {
		const rounds, callers = 60, 8
		for r := range rounds {
			key := fmt.Sprintf("last-%d", r)
			var allowed int
			var mu sync.Mutex
			start := make(chan struct{})
			var wg sync.WaitGroup
			for range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					d, err := s.Take(ctx, key, base, 1, time.Hour)
					if err != nil {
						t.Errorf("Take returned the error %v", err)
						return
					}
					if d.Allowed {
						mu.Lock()
						allowed++
						mu.Unlock()
					}
				}()
			}
			close(start)
			wg.Wait()
			if allowed != 1 {
				t.Fatalf("round %d: %d of %d concurrent callers got the single request of a burst of 1", r, allowed, callers)
			}
		}
	})
	run("concurrent callers of one key are allowed exactly burst requests in all", func(t *testing.T, s bites.RateLimitStore) {
		const burst, callers = 5, 40
		var mu sync.Mutex
		allowed, maxRemaining := 0, -1
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				d, err := s.Take(ctx, "k", base, burst, time.Hour)
				if err != nil {
					t.Errorf("Take returned the error %v", err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if d.Allowed {
					allowed++
					maxRemaining = max(maxRemaining, d.Remaining)
				}
			}()
		}
		close(start)
		wg.Wait()
		if allowed != burst || maxRemaining != burst-1 {
			t.Fatalf("%d of %d concurrent callers were allowed (the highest Remaining was %d), want exactly %d (and %d)", allowed, callers, maxRemaining, burst, burst-1)
		}
	})
	run("concurrent callers of different keys do not disturb each other", func(t *testing.T, s bites.RateLimitStore) {
		const keys, callers, burst = 12, 4, 2
		var mu sync.Mutex
		allowed := map[string]int{}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for k := range keys {
			for range callers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					key := "key-" + strconv.Itoa(k)
					d, err := s.Take(ctx, key, base, burst, time.Hour)
					if err != nil {
						t.Errorf("Take returned the error %v", err)
						return
					}
					if d.Allowed {
						mu.Lock()
						allowed[key]++
						mu.Unlock()
					}
				}()
			}
		}
		close(start)
		wg.Wait()
		for k := range keys {
			if n := allowed["key-"+strconv.Itoa(k)]; n != burst {
				t.Errorf("key-%d had %d requests allowed, want %d", k, n, burst)
			}
		}
	})
}

// TestSessionStore checks a bites.SessionStore against the contract of its doc comment, one
// subtest a promise: Load of an id that was never saved, was deleted or has expired is not found,
// Save keeps data for an id until its expiry and replaces what the id had (the expiry too),
// Delete of an id with nothing is not an error, and ids do not share data. newStore is called once
// for each subtest and must return a store with no state of any earlier one; it may call
// t.Cleanup. The suite calls Save, so a store written only for owned types, whose Save returns an
// error, is checked by TestOwnerSessionStore alone. Expiry is judged on bites.Now(ctx), the clock of
// the app whose request made the call, and not on the store's own clock or the database's: the
// suite gives every call a context of an app on a bitetest.Clock, gives expiries relative to it,
// and moves it with Advance instead of sleeping, so a store that reads time.Now() finds the
// sessions of a clock set in 2026 expired at once and fails. A store that hands the expiry to a
// server (a Redis TTL) computes it as expires minus bites.Now(ctx). A nil newStore fails the test.
func TestSessionStore(t *testing.T, newStore func(t *testing.T) bites.SessionStore) {
	t.Helper()
	if newStore == nil {
		t.Fatal("bitetest.TestSessionStore: newStore is nil: give a function that returns a new bites.SessionStore for each subtest")
	}
	var ctx context.Context // the app's, on clk: set by run for each subtest
	var clk *Clock
	hour := func() time.Time { return clk.Now().Add(time.Hour) }
	run := func(name string, f func(t *testing.T, s bites.SessionStore)) {
		t.Run(name, func(t *testing.T) {
			ctx, clk = clockContext(t)
			f(t, newStore(t))
		})
	}
	load := func(t *testing.T, s bites.SessionStore, id string) ([]byte, bool) {
		t.Helper()
		d, ok, err := s.Load(ctx, id)
		if err != nil {
			t.Fatalf("Load(%q) returned the error %v", id, err)
		}
		return d, ok
	}
	save := func(t *testing.T, s bites.SessionStore, id string, data []byte, exp time.Time) {
		t.Helper()
		if err := s.Save(ctx, id, data, exp); err != nil {
			t.Fatalf("Save(%q) returned the error %v", id, err)
		}
	}

	run("Load of an id that was never saved is not found and not an error", func(t *testing.T, s bites.SessionStore) {
		if d, ok := load(t, s, "never-saved-id-0123456"); ok || d != nil {
			t.Fatalf("Load of an unknown id = %q, %v", d, ok)
		}
	})
	run("a saved session is loaded with its data", func(t *testing.T, s bites.SessionStore) {
		save(t, s, "id-1", []byte(`{"a":1}`), hour())
		if d, ok := load(t, s, "id-1"); !ok || string(d) != `{"a":1}` {
			t.Fatalf("Load = %q, %v", d, ok)
		}
		if _, ok := load(t, s, "id-2"); ok {
			t.Fatal("an id that was never saved was found")
		}
	})
	run("data is kept byte for byte, binary, empty and large", func(t *testing.T, s bites.SessionStore) {
		binary := []byte{0, 1, 2, 0xff, 0xfe, 0, '\n', '"'}
		large := bytes.Repeat([]byte("0123456789abcdef"), 64*1024)
		save(t, s, "binary", binary, hour())
		save(t, s, "large", large, hour())
		save(t, s, "empty", []byte{}, hour())
		if d, ok := load(t, s, "binary"); !ok || !bytes.Equal(d, binary) {
			t.Fatalf("binary data came back as %q, %v", d, ok)
		}
		if d, ok := load(t, s, "large"); !ok || !bytes.Equal(d, large) {
			t.Fatalf("1 MiB of data came back as %d bytes, %v", len(d), ok)
		}
		if d, ok := load(t, s, "empty"); !ok || len(d) != 0 {
			t.Fatalf("empty data came back as %q, found %v: a session with no bytes is still a session", d, ok)
		}
	})
	run("Save replaces what the id had, data and expiry", func(t *testing.T, s bites.SessionStore) {
		save(t, s, "id-1", []byte("first"), clk.Now().Add(-time.Hour))
		if _, ok := load(t, s, "id-1"); ok {
			t.Fatal("a session saved already expired was found")
		}
		save(t, s, "id-1", []byte("second"), hour())
		if d, ok := load(t, s, "id-1"); !ok || string(d) != "second" {
			t.Fatalf("a Save over an expired session: Load = %q, %v", d, ok)
		}
		save(t, s, "id-1", []byte("third"), hour())
		if d, ok := load(t, s, "id-1"); !ok || string(d) != "third" {
			t.Fatalf("a Save over a live session: Load = %q, %v", d, ok)
		}
		save(t, s, "id-1", []byte("fourth"), clk.Now().Add(-time.Hour))
		if _, ok := load(t, s, "id-1"); ok {
			t.Fatal("a Save with an expiry in the past did not replace the earlier expiry")
		}
	})
	run("a session whose expiry is in the past is not found", func(t *testing.T, s bites.SessionStore) {
		save(t, s, "past", []byte("x"), clk.Now().Add(-time.Second))
		if d, ok := load(t, s, "past"); ok {
			t.Fatalf("an expired session was found: %q", d)
		}
	})
	run("a session is found until its expiry and not after", func(t *testing.T, s bites.SessionStore) {
		save(t, s, "soon", []byte("x"), clk.Now().Add(1500*time.Millisecond))
		save(t, s, "later", []byte("y"), hour())
		if _, ok := load(t, s, "soon"); !ok {
			t.Fatal("a session was not found before its expiry")
		}
		clk.Advance(1700 * time.Millisecond)
		if d, ok := load(t, s, "soon"); ok {
			t.Fatalf("a session was found after its expiry: %q", d)
		}
		if _, ok := load(t, s, "later"); !ok {
			t.Fatal("a session with a later expiry was lost")
		}
	})
	run("Delete forgets an id and leaves the others", func(t *testing.T, s bites.SessionStore) {
		save(t, s, "id-1", []byte("one"), hour())
		save(t, s, "id-2", []byte("two"), hour())
		if err := s.Delete(ctx, "id-1"); err != nil {
			t.Fatalf("Delete returned %v", err)
		}
		if _, ok := load(t, s, "id-1"); ok {
			t.Fatal("a deleted session was found")
		}
		if d, ok := load(t, s, "id-2"); !ok || string(d) != "two" {
			t.Fatalf("Delete of one id changed another: %q, %v", d, ok)
		}
		save(t, s, "id-1", []byte("again"), hour())
		if d, ok := load(t, s, "id-1"); !ok || string(d) != "again" {
			t.Fatalf("an id saved again after Delete: %q, %v", d, ok)
		}
	})
	run("Delete of an id that has nothing, or that was already deleted, is not an error", func(t *testing.T, s bites.SessionStore) {
		if err := s.Delete(ctx, "never-saved-id-0123456"); err != nil {
			t.Fatalf("Delete of an unknown id returned %v", err)
		}
		save(t, s, "id-1", []byte("x"), hour())
		for range 2 {
			if err := s.Delete(ctx, "id-1"); err != nil {
				t.Fatalf("Delete returned %v", err)
			}
		}
	})
	run("ids that differ in one character do not share a session", func(t *testing.T, s bites.SessionStore) {
		ids := []string{"AAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAB", "aAAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAA", "AAAAAAAAAAAAAAAAAAAAAAA"}
		for _, id := range ids {
			save(t, s, id, []byte(id), hour())
		}
		for _, id := range ids {
			if d, ok := load(t, s, id); !ok || string(d) != id {
				t.Fatalf("Load(%q) = %q, %v", id, d, ok)
			}
		}
	})
	run("concurrent saves, loads and deletes of many ids are safe and keep each id's own data", func(t *testing.T, s bites.SessionStore) {
		const n = 16
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := "conc-" + strconv.Itoa(i)
				for round := range 10 {
					want := []byte(id + "/" + strconv.Itoa(round))
					if err := s.Save(ctx, id, want, hour()); err != nil {
						t.Errorf("Save returned %v", err)
						return
					}
					d, ok, err := s.Load(ctx, id)
					if err != nil || !ok || !bytes.Equal(d, want) {
						t.Errorf("Load(%q) = %q, %v, %v, want %q", id, d, ok, err, want)
						return
					}
				}
				if err := s.Delete(ctx, id); err != nil {
					t.Errorf("Delete returned %v", err)
				}
			}()
		}
		wg.Wait()
		for i := range n {
			if _, ok := load(t, s, "conc-"+strconv.Itoa(i)); ok {
				t.Errorf("conc-%d was found after its Delete", i)
			}
		}
	})
}

// OwnerSessionSuite is what TestOwnerSessionStore needs beyond the store when the store cannot take
// the owners the suite would choose. The zero value is the default: the suite saves sessions for
// owners it names itself, empty included.
type OwnerSessionSuite struct {
	// Owner returns the owner string the suite saves sessions under for name, one of the eight
	// names the suite uses ("alice", "bob", "nobody", "Alice", "alice ", "ali", "alice@example.com"
	// and "café"). It is called in each subtest after newStore, with that subtest's store, at most
	// once for a name, so it can create a real row for the owner (a users row that the sessions'
	// owner column refers to) and register its removal with t.Cleanup. It must return a non-empty
	// string, and a different one for each name: the suite fails the test with a message that says
	// which name broke the rule. Returning name itself is right for a column of text. Where the
	// owner is an id that cannot be spelled like the names (a UUID), return one for each name; the
	// subtest about owners that differ in case, space or prefix then holds the ids apart and no
	// more. Nil means the suite uses the names as they are, which a store that accepts any owner
	// needs nothing else for.
	Owner func(t *testing.T, s bites.OwnerSessionStore, name string) string
	// NoEmptyOwner is true for a store that refuses an empty owner, as a foreign key to a users
	// table does. The subtest about a session saved with an empty owner is then skipped, and the
	// skip says why in the test output. The store then cannot keep an anonymous session: the
	// application's session type must never return "" from SessionOwner, because bites cannot
	// tell before the first request (the owner is read from each session value at Save and
	// Renew, and NewSessions sees only the type), so such a session surfaces as the store's error
	// at the request that saves it. False means the store keeps a session with an empty owner
	// that DeleteOwner never finds, as the interface says.
	NoEmptyOwner bool
}

// TestOwnerSessionStore checks a bites.OwnerSessionStore against the contract of its doc comment,
// one subtest a promise: SaveOwned keeps data and records the owner, replacing the owner the id had,
// an empty owner is a session that is kept and that DeleteOwner never finds, and DeleteOwner
// forgets every session of an owner but keep and returns how many, with keep empty, keep a
// session of the owner, keep an id of another owner or of none, and an owner with no session. It
// also holds that a Delete or an expiry ends an id's link to its owner. The SessionStore half of
// the contract is TestSessionStore; call both. newStore is called once for each subtest and must
// return a store with no state of any earlier one; it may call t.Cleanup. A nil newStore fails the
// test. A store whose owners must be real rows, or that refuses an empty owner, passes one
// OwnerSessionSuite; more than one fails the test.
func TestOwnerSessionStore(t *testing.T, newStore func(t *testing.T) bites.OwnerSessionStore, suite ...OwnerSessionSuite) {
	t.Helper()
	if newStore == nil {
		t.Fatal("bitetest.TestOwnerSessionStore: newStore is nil: give a function that returns a new bites.OwnerSessionStore for each subtest")
	}
	if len(suite) > 1 {
		t.Fatalf("bitetest.TestOwnerSessionStore: %d OwnerSessionSuite values given: give one, with the Owner and NoEmptyOwner the store needs", len(suite))
	}
	var cfg OwnerSessionSuite
	if len(suite) == 1 {
		cfg = suite[0]
	}
	var ctx context.Context // the app's, on clk: set by run for each subtest
	var clk *Clock
	var own func(t *testing.T, name string) string // the owner for name, set by run for each subtest
	hour := func() time.Time { return clk.Now().Add(time.Hour) }
	run := func(name string, f func(t *testing.T, s bites.OwnerSessionStore)) {
		t.Run(name, func(t *testing.T) {
			ctx, clk = clockContext(t)
			s := newStore(t)
			given, names := map[string]string{}, map[string]string{} // name -> owner, owner -> name
			own = func(t *testing.T, name string) string {
				t.Helper()
				if name == "" {
					return ""
				}
				if cfg.Owner == nil {
					return name
				}
				if o, ok := given[name]; ok {
					return o
				}
				o := cfg.Owner(t, s, name)
				if o == "" {
					t.Fatalf("bitetest.TestOwnerSessionStore: OwnerSessionSuite.Owner returned an empty owner for %q: return the id of a real owner, and set NoEmptyOwner if the store refuses an empty one", name)
				}
				if prev, dup := names[o]; dup {
					t.Fatalf("bitetest.TestOwnerSessionStore: OwnerSessionSuite.Owner returned %q for both %q and %q: return a different owner for each name", o, prev, name)
				}
				given[name], names[o] = o, name
				return o
			}
			f(t, s)
		})
	}
	saveOwned := func(t *testing.T, s bites.OwnerSessionStore, id, owner string) {
		t.Helper()
		if err := s.SaveOwned(ctx, id, own(t, owner), []byte("data-"+id), hour()); err != nil {
			t.Fatalf("SaveOwned(%q, %q) returned the error %v", id, owner, err)
		}
	}
	found := func(t *testing.T, s bites.OwnerSessionStore, id string) bool {
		t.Helper()
		_, ok, err := s.Load(ctx, id)
		if err != nil {
			t.Fatalf("Load(%q) returned the error %v", id, err)
		}
		return ok
	}
	deleteOwner := func(t *testing.T, s bites.OwnerSessionStore, owner, keep string) int {
		t.Helper()
		n, err := s.DeleteOwner(ctx, own(t, owner), keep)
		if err != nil {
			t.Fatalf("DeleteOwner(%q, %q) returned the error %v", owner, keep, err)
		}
		return n
	}

	run("SaveOwned keeps the data for the id until its expiry", func(t *testing.T, s bites.OwnerSessionStore) {
		saveOwned(t, s, "id-1", "alice")
		if d, ok, err := s.Load(ctx, "id-1"); err != nil || !ok || string(d) != "data-id-1" {
			t.Fatalf("Load = %q, %v, %v", d, ok, err)
		}
		if err := s.SaveOwned(ctx, "id-2", own(t, "alice"), []byte("x"), clk.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if found(t, s, "id-2") {
			t.Fatal("a session saved already expired was found")
		}
	})
	run("SaveOwned replaces the data, the expiry and the owner the id had", func(t *testing.T, s bites.OwnerSessionStore) {
		saveOwned(t, s, "id-1", "alice")
		if err := s.SaveOwned(ctx, "id-1", own(t, "bob"), []byte("new"), hour()); err != nil {
			t.Fatal(err)
		}
		if d, ok, _ := s.Load(ctx, "id-1"); !ok || string(d) != "new" {
			t.Fatalf("Load after a second SaveOwned = %q, %v", d, ok)
		}
		if n := deleteOwner(t, s, "alice", ""); n != 0 {
			t.Fatalf("DeleteOwner of the owner the id left forgot %d sessions, want 0", n)
		}
		if !found(t, s, "id-1") {
			t.Fatal("DeleteOwner of the owner the id left forgot the id")
		}
		if n := deleteOwner(t, s, "bob", ""); n != 1 || found(t, s, "id-1") {
			t.Fatalf("DeleteOwner of the new owner forgot %d sessions, want 1 and the id gone", n)
		}
	})
	run("a session saved with an empty owner is kept and DeleteOwner never finds it", func(t *testing.T, s bites.OwnerSessionStore) {
		if cfg.NoEmptyOwner {
			t.Skip("OwnerSessionSuite.NoEmptyOwner is set: the store refuses an empty owner, so it cannot keep an anonymous session, and the application's SessionOwner must never return \"\"")
		}
		saveOwned(t, s, "anon-1", "")
		saveOwned(t, s, "anon-2", "")
		if !found(t, s, "anon-1") || !found(t, s, "anon-2") {
			t.Fatal("a session with an empty owner was not kept")
		}
		for _, keep := range []string{"", "anon-1", "other"} {
			if n := deleteOwner(t, s, "", keep); n != 0 {
				t.Fatalf("DeleteOwner(\"\", %q) forgot %d sessions, want 0: an empty owner is no one", keep, n)
			}
		}
		if !found(t, s, "anon-1") || !found(t, s, "anon-2") {
			t.Fatal("DeleteOwner of the empty owner forgot a session of no owner")
		}
		saveOwned(t, s, "alice-1", "alice")
		if n := deleteOwner(t, s, "alice", ""); n != 1 || !found(t, s, "anon-1") {
			t.Fatalf("DeleteOwner(alice) forgot %d sessions or touched a session of no owner", n)
		}
	})
	run("DeleteOwner with keep empty forgets every session of the owner and returns how many", func(t *testing.T, s bites.OwnerSessionStore) {
		for i := range 5 {
			saveOwned(t, s, "alice-"+strconv.Itoa(i), "alice")
		}
		saveOwned(t, s, "bob-1", "bob")
		if n := deleteOwner(t, s, "alice", ""); n != 5 {
			t.Fatalf("DeleteOwner forgot %d sessions, want 5", n)
		}
		for i := range 5 {
			if found(t, s, "alice-"+strconv.Itoa(i)) {
				t.Fatalf("alice-%d survived DeleteOwner", i)
			}
		}
		if !found(t, s, "bob-1") {
			t.Fatal("DeleteOwner(alice) forgot a session of bob")
		}
		if n := deleteOwner(t, s, "alice", ""); n != 0 {
			t.Fatalf("a second DeleteOwner forgot %d sessions, want 0", n)
		}
	})
	run("DeleteOwner keeps the session whose id is keep and forgets the owner's others", func(t *testing.T, s bites.OwnerSessionStore) {
		for i := range 4 {
			saveOwned(t, s, "alice-"+strconv.Itoa(i), "alice")
		}
		if n := deleteOwner(t, s, "alice", "alice-2"); n != 3 {
			t.Fatalf("DeleteOwner forgot %d sessions, want 3", n)
		}
		for i := range 4 {
			if got := found(t, s, "alice-"+strconv.Itoa(i)); got != (i == 2) {
				t.Fatalf("alice-%d found = %v after DeleteOwner keeping alice-2", i, got)
			}
		}
		if n := deleteOwner(t, s, "alice", "alice-2"); n != 0 {
			t.Fatalf("DeleteOwner with only the kept session left forgot %d, want 0", n)
		}
	})
	run("a keep that is not a session of the owner keeps nothing of the owner's", func(t *testing.T, s bites.OwnerSessionStore) {
		saveOwned(t, s, "alice-1", "alice")
		saveOwned(t, s, "alice-2", "alice")
		saveOwned(t, s, "bob-1", "bob")
		if n := deleteOwner(t, s, "alice", "bob-1"); n != 2 {
			t.Fatalf("DeleteOwner(alice, a session of bob) forgot %d sessions, want 2", n)
		}
		if found(t, s, "alice-1") || found(t, s, "alice-2") {
			t.Fatal("a session of alice survived a keep that names a session of bob")
		}
		if !found(t, s, "bob-1") {
			t.Fatal("DeleteOwner(alice, a session of bob) forgot the session of bob")
		}
		saveOwned(t, s, "alice-3", "alice")
		if n := deleteOwner(t, s, "alice", "no-such-id"); n != 1 || found(t, s, "alice-3") {
			t.Fatalf("DeleteOwner(alice, an unknown id) forgot %d sessions, want 1", n)
		}
	})
	run("an owner with no session is zero and not an error", func(t *testing.T, s bites.OwnerSessionStore) {
		if n := deleteOwner(t, s, "nobody", ""); n != 0 {
			t.Fatalf("DeleteOwner of an unknown owner returned %d", n)
		}
		if n := deleteOwner(t, s, "nobody", "an-id"); n != 0 {
			t.Fatalf("DeleteOwner of an unknown owner with a keep returned %d", n)
		}
	})
	run("owners that differ only in case, space or a prefix are different owners", func(t *testing.T, s bites.OwnerSessionStore) {
		owners := []string{"alice", "Alice", "alice ", "ali", "alice@example.com", "café"}
		for _, o := range owners {
			own(t, o) // every owner exists before the first save, and a bad Owner fails here
		}
		for i, o := range owners {
			saveOwned(t, s, "id-"+strconv.Itoa(i), o)
		}
		if n := deleteOwner(t, s, "alice", ""); n != 1 {
			t.Fatalf("DeleteOwner(alice) forgot %d sessions, want 1", n)
		}
		for i, o := range owners {
			if got := found(t, s, "id-"+strconv.Itoa(i)); got != (o != "alice") {
				t.Fatalf("session of %q found = %v after DeleteOwner(alice)", o, got)
			}
		}
	})
	run("Delete ends the id's link to its owner, so DeleteOwner does not count it", func(t *testing.T, s bites.OwnerSessionStore) {
		saveOwned(t, s, "alice-1", "alice")
		saveOwned(t, s, "alice-2", "alice")
		if err := s.Delete(ctx, "alice-1"); err != nil {
			t.Fatal(err)
		}
		if n := deleteOwner(t, s, "alice", ""); n != 1 {
			t.Fatalf("DeleteOwner forgot %d sessions after one was deleted, want 1", n)
		}
	})
	run("a session saved again with an owner after DeleteOwner belongs to that owner", func(t *testing.T, s bites.OwnerSessionStore) {
		saveOwned(t, s, "alice-1", "alice")
		deleteOwner(t, s, "alice", "")
		saveOwned(t, s, "alice-1", "alice")
		if !found(t, s, "alice-1") {
			t.Fatal("a session saved after DeleteOwner was not found")
		}
		if n := deleteOwner(t, s, "alice", ""); n != 1 {
			t.Fatalf("DeleteOwner forgot %d sessions, want 1", n)
		}
	})
	run("concurrent SaveOwned and DeleteOwner of one owner are safe and end with none", func(t *testing.T, s bites.OwnerSessionStore) {
		alice := own(t, "alice")
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for r := range 10 {
					id := fmt.Sprintf("alice-%d-%d", i, r)
					if err := s.SaveOwned(ctx, id, alice, []byte(id), hour()); err != nil {
						t.Errorf("SaveOwned returned %v", err)
						return
					}
					if _, err := s.DeleteOwner(ctx, alice, ""); err != nil {
						t.Errorf("DeleteOwner returned %v", err)
						return
					}
				}
			}()
		}
		wg.Wait()
		deleteOwner(t, s, "alice", "")
		if n := deleteOwner(t, s, "alice", ""); n != 0 {
			t.Fatalf("a session of alice survived the last DeleteOwner: %d left", n)
		}
	})
}

// BrokerSuite is what TestBroker needs to check a bites.Broker. The zero value is not usable: New
// is required.
type BrokerSuite struct {
	// New returns two Brokers that reach each other, the instances of one application: a Publish
	// on either is heard by the subscriptions of both, as on one bus, channel or database. It is
	// called once for each subtest, which must hear nothing of an earlier one, and it starts what
	// the Brokers need to deliver and ends it with t.Cleanup. Required.
	New func(t *testing.T) (a, b bites.Broker)
	// Name is the Topic name every subtest publishes to. It must be a name the Brokers accept.
	// Empty means "conformance.topic".
	Name string
	// Refused lists names whose Accepts must be an error, so the suite checks a Broker does
	// refuse what it cannot carry. Empty skips that subtest.
	Refused []string
	// MaxPayload is the largest payload, in bytes, a Publish to Name carries; the suite publishes
	// one of that size and one a byte larger, which must be a *bites.PayloadError (found with
	// errors.As) with this Name, the larger size and a Limit of exactly MaxPayload, and delivers
	// nothing. A limit that counts the Topic name, as Postgres's notification does,
	// gives the payload room that is left. Zero means the Broker has no limit the suite can
	// reach, and skips that subtest.
	MaxPayload int
	// Wait is how long the suite waits for a delivery before it fails, and how long it listens
	// for one that must not come. Zero means 5 seconds to wait and 100 milliseconds to listen.
	Wait time.Duration
}

// TestBroker checks a bites.Broker against the contract of its doc comment, one subtest a promise:
// Accepts is nil for a name the Broker carries and an error for one it cannot; Publish reaches
// the subscriptions of a peer and of the publishing Broker, once each, in the order of the calls
// of one publisher; names are separate streams; each payload given to deliver is its own bytes,
// kept when the publisher's slice is reused and when another subscriber changes its copy; a
// payload over the limit is a *PayloadError and sends nothing; and stop ends
// delivery to one subscription and no other. The payloads the suite publishes are JSON, as a
// Topic's are, so a Broker that carries only text is checked fairly. See BrokerSuite for what the
// suite needs.
func TestBroker(t *testing.T, s BrokerSuite) {
	t.Helper()
	if s.New == nil {
		t.Fatal("bitetest.TestBroker: BrokerSuite.New is nil: give a function that returns two Brokers that reach each other for each subtest")
	}
	name := s.Name
	if name == "" {
		name = "conformance.topic"
	}
	wait, quiet := s.Wait, s.Wait/50
	if wait == 0 {
		wait, quiet = 5*time.Second, 100*time.Millisecond
	}
	ctx := context.Background()
	run := func(label string, f func(t *testing.T, a, b bites.Broker)) {
		t.Run(label, func(t *testing.T) {
			a, b := s.New(t)
			f(t, a, b)
		})
	}
	// sink collects what a subscription is delivered, safe for any goroutine.
	type sink struct {
		mu  sync.Mutex
		got [][]byte
		ch  chan struct{}
	}
	newSink := func() *sink { return &sink{ch: make(chan struct{}, 4096)} }
	deliver := func(k *sink) func([]byte) {
		return func(p []byte) {
			k.mu.Lock()
			k.got = append(k.got, p)
			k.mu.Unlock()
			select {
			case k.ch <- struct{}{}:
			default:
			}
		}
	}
	gap := func() {}
	snapshot := func(k *sink) []string {
		k.mu.Lock()
		defer k.mu.Unlock()
		out := make([]string, len(k.got))
		for i, p := range k.got {
			out[i] = string(p)
		}
		return out
	}
	awaitCount := func(t *testing.T, k *sink, n int) {
		t.Helper()
		deadline := time.After(wait)
		for {
			k.mu.Lock()
			have := len(k.got)
			k.mu.Unlock()
			if have >= n {
				return
			}
			select {
			case <-k.ch:
			case <-deadline:
				t.Fatalf("waited %v for %d deliveries and got %d: %q", wait, n, have, snapshot(k))
			}
		}
	}
	publish := func(t *testing.T, b bites.Broker, name string, payload string) {
		t.Helper()
		if err := b.Publish(ctx, name, []byte(payload)); err != nil {
			t.Fatalf("Publish(%q, %s) returned the error %v", name, payload, err)
		}
	}
	json := func(s string) string { return strconv.Quote(s) }

	t.Run("Accepts is nil for a name the Broker carries", func(t *testing.T) {
		a, b := s.New(t)
		for _, br := range []bites.Broker{a, b} {
			if err := br.Accepts(name); err != nil {
				t.Fatalf("Accepts(%q) = %v, want nil for the name the suite publishes to", name, err)
			}
		}
	})
	if len(s.Refused) > 0 {
		run("Accepts refuses a name the Broker cannot carry, with an error", func(t *testing.T, a, b bites.Broker) {
			for _, n := range s.Refused {
				for _, br := range []bites.Broker{a, b} {
					if err := br.Accepts(n); err == nil {
						t.Errorf("Accepts(%q) = nil, want an error: NewTopic would accept a name the Broker cannot route", n)
					}
				}
			}
		})
	}
	run("a Publish reaches a subscription on a peer Broker", func(t *testing.T, a, b bites.Broker) {
		k := newSink()
		stop := b.Subscribe(name, deliver(k), gap)
		defer stop()
		publish(t, a, name, json("to the peer"))
		awaitCount(t, k, 1)
		if got := snapshot(k); len(got) != 1 || got[0] != json("to the peer") {
			t.Fatalf("the peer was delivered %q", got)
		}
	})
	run("a Publish reaches a subscription on the publishing Broker itself", func(t *testing.T, a, b bites.Broker) {
		k := newSink()
		stop := a.Subscribe(name, deliver(k), gap)
		defer stop()
		publish(t, a, name, json("to myself"))
		awaitCount(t, k, 1)
		if got := snapshot(k); len(got) != 1 || got[0] != json("to myself") {
			t.Fatalf("the publisher was delivered %q", got)
		}
	})
	run("a Publish reaches every subscription of every Broker exactly once", func(t *testing.T, a, b bites.Broker) {
		sinks := []*sink{newSink(), newSink(), newSink(), newSink()}
		for i, br := range []bites.Broker{a, a, b, b} {
			defer br.Subscribe(name, deliver(sinks[i]), gap)()
		}
		publish(t, b, name, json("one"))
		for _, k := range sinks {
			awaitCount(t, k, 1)
		}
		time.Sleep(quiet)
		for i, k := range sinks {
			if got := snapshot(k); len(got) != 1 || got[0] != json("one") {
				t.Errorf("subscription %d was delivered %q, want the one payload once", i, got)
			}
		}
	})
	run("payloads of one publisher arrive in the order of its Publish calls, on the publisher and on a peer", func(t *testing.T, a, b bites.Broker) {
		const n = 200
		ka, kb := newSink(), newSink()
		defer a.Subscribe(name, deliver(ka), gap)()
		defer b.Subscribe(name, deliver(kb), gap)()
		for i := range n {
			publish(t, a, name, strconv.Itoa(i))
		}
		awaitCount(t, ka, n)
		awaitCount(t, kb, n)
		time.Sleep(quiet)
		for label, k := range map[string]*sink{"publisher": ka, "peer": kb} {
			got := snapshot(k)
			if len(got) != n {
				t.Fatalf("the %s was delivered %d payloads, want %d", label, len(got), n)
			}
			for i, p := range got {
				if p != strconv.Itoa(i) {
					t.Fatalf("the %s was delivered %q at position %d: out of order or doubled", label, p, i)
				}
			}
		}
	})
	run("two publishers at once lose nothing and each one's payloads stay in order", func(t *testing.T, a, b bites.Broker) {
		const n = 100
		ka, kb := newSink(), newSink()
		defer a.Subscribe(name, deliver(ka), gap)()
		defer b.Subscribe(name, deliver(kb), gap)()
		var wg sync.WaitGroup
		for label, br := range map[string]bites.Broker{"a": a, "b": b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range n {
					if err := br.Publish(ctx, name, []byte(json(label+strconv.Itoa(i)))); err != nil {
						t.Errorf("Publish returned %v", err)
						return
					}
				}
			}()
		}
		wg.Wait()
		awaitCount(t, ka, 2*n)
		awaitCount(t, kb, 2*n)
		time.Sleep(quiet)
		for which, k := range map[string]*sink{"a's subscription": ka, "b's subscription": kb} {
			got := snapshot(k)
			next := map[string]int{"a": 0, "b": 0}
			for _, p := range got {
				who := string(p[1])
				if p != json(who+strconv.Itoa(next[who])) {
					t.Fatalf("%s was delivered %s where %s%d was next: a publisher's payloads are out of order or lost", which, p, who, next[who])
				}
				next[who]++
			}
			if len(got) != 2*n {
				t.Fatalf("%s was delivered %d payloads, want %d", which, len(got), 2*n)
			}
		}
	})
	run("a payload goes only to subscriptions of its name", func(t *testing.T, a, b bites.Broker) {
		other := newSink()
		k := newSink()
		defer b.Subscribe(name+".other", deliver(other), gap)()
		defer b.Subscribe(name, deliver(k), gap)()
		publish(t, a, name, json("mine"))
		awaitCount(t, k, 1)
		time.Sleep(quiet)
		if got := snapshot(other); len(got) != 0 {
			t.Fatalf("a subscription to another name was delivered %q", got)
		}
		publish(t, a, name+".other", json("theirs"))
		awaitCount(t, other, 1)
		time.Sleep(quiet)
		if got := snapshot(k); len(got) != 1 {
			t.Fatalf("a subscription was delivered a payload of another name: %q", got)
		}
	})
	run("each payload given to deliver is its own bytes, unchanged by the publisher reusing its slice or a subscriber changing its copy", func(t *testing.T, a, b bites.Broker) {
		k1, k2 := newSink(), newSink()
		defer b.Subscribe(name, deliver(k1), gap)()
		defer b.Subscribe(name, deliver(k2), gap)()
		buf := []byte(json("first payload"))
		if err := a.Publish(ctx, name, buf); err != nil {
			t.Fatal(err)
		}
		for i := range buf { // the publisher reuses its slice once Publish has returned
			buf[i] = 'X'
		}
		awaitCount(t, k1, 1)
		awaitCount(t, k2, 1)
		k1.mu.Lock()
		for i := range k1.got[0] { // a subscriber scribbles on what it was given
			k1.got[0][i] = 'Y'
		}
		k1.mu.Unlock()
		publish(t, a, name, json("second payload"))
		awaitCount(t, k1, 2)
		awaitCount(t, k2, 2)
		if got := snapshot(k2); got[0] != json("first payload") || got[1] != json("second payload") {
			t.Fatalf("the second subscriber was delivered %q: bytes were shared with the publisher's slice or another subscriber's", got)
		}
		if got := snapshot(k1); got[1] != json("second payload") {
			t.Fatalf("the first subscriber's second payload is %q", got[1])
		}
	})
	run("a payload kept by a subscriber is not changed by later payloads", func(t *testing.T, a, b bites.Broker) {
		const n = 50
		k := newSink()
		defer b.Subscribe(name, deliver(k), gap)()
		for i := range n {
			publish(t, a, name, json("payload number "+strconv.Itoa(i)))
		}
		awaitCount(t, k, n)
		time.Sleep(quiet)
		for i, p := range snapshot(k) {
			if p != json("payload number "+strconv.Itoa(i)) {
				t.Fatalf("payload %d reads %q after the later ones arrived: the Broker reused a buffer", i, p)
			}
		}
	})
	run("stop ends delivery to its subscription and to no other", func(t *testing.T, a, b bites.Broker) {
		stopped, kept := newSink(), newSink()
		stop := b.Subscribe(name, deliver(stopped), gap)
		defer b.Subscribe(name, deliver(kept), gap)()
		publish(t, a, name, json("before"))
		awaitCount(t, stopped, 1)
		awaitCount(t, kept, 1)
		stop()
		publish(t, a, name, json("after"))
		awaitCount(t, kept, 2)
		time.Sleep(quiet)
		if got := snapshot(stopped); len(got) != 1 {
			t.Fatalf("a stopped subscription was delivered %q", got)
		}
		if got := snapshot(kept); len(got) != 2 || got[1] != json("after") {
			t.Fatalf("the other subscription was delivered %q", got)
		}
	})
	run("a subscription stopped at once is delivered nothing", func(t *testing.T, a, b bites.Broker) {
		first := newSink()
		b.Subscribe(name, deliver(first), gap)()
		k := newSink()
		defer b.Subscribe(name, deliver(k), gap)()
		publish(t, a, name, json("again"))
		awaitCount(t, k, 1)
		time.Sleep(quiet)
		if got := snapshot(first); len(got) != 0 {
			t.Fatalf("a subscription stopped at once was delivered %q", got)
		}
	})
	if s.MaxPayload > 0 {
		padded := func(n int) string { return `"` + strings.Repeat("p", n-2) + `"` }
		run("a payload of exactly the limit is carried whole", func(t *testing.T, a, b bites.Broker) {
			k := newSink()
			defer b.Subscribe(name, deliver(k), gap)()
			want := padded(s.MaxPayload)
			publish(t, a, name, want)
			awaitCount(t, k, 1)
			if got := snapshot(k)[0]; got != want {
				t.Fatalf("a payload of %d bytes, the limit, was delivered as %d bytes: a Broker must not truncate", len(want), len(got))
			}
		})
		run("a payload over the limit is a *PayloadError naming the name, the size and the limit left to the payload, and nothing is sent", func(t *testing.T, a, b bites.Broker) {
			k, own := newSink(), newSink()
			defer b.Subscribe(name, deliver(k), gap)()
			defer a.Subscribe(name, deliver(own), gap)()
			over := padded(s.MaxPayload + 1)
			err := a.Publish(ctx, name, []byte(over))
			if err == nil {
				t.Fatalf("a payload of %d bytes, one over the limit, was accepted: a Broker must not truncate", len(over))
			}
			var pe *bites.PayloadError
			if !errors.As(err, &pe) {
				t.Fatalf("the error of a payload over the limit is %T (%v), not a *bites.PayloadError: a program cannot tell it from a Broker that is down", err, err)
			}
			if pe.Name != name || pe.Size != len(over) || pe.Limit != s.MaxPayload {
				t.Errorf("the *PayloadError is {Name: %q, Size: %d, Limit: %d}, want {Name: %q, Size: %d, Limit: %d}: Limit is what is left to the payload of this name, with what the Broker counts besides taken off", pe.Name, pe.Size, pe.Limit, name, len(over), s.MaxPayload)
			}
			time.Sleep(quiet)
			if len(snapshot(k)) != 0 || len(snapshot(own)) != 0 {
				t.Fatalf("a refused payload was delivered: peer %q, publisher %q", snapshot(k), snapshot(own))
			}
			publish(t, a, name, json("still works"))
			awaitCount(t, k, 1)
		})
	}
}

// clockContext returns a context that bites.Now reads as clk, a new Clock, which is what the
// context of a request or of a Service's Start is. It starts an App on clk with one Service that
// keeps the context of its Start, and the test's cleanup stops the App and so ends the context.
// A conformance suite hands it to a store, so the store's judgment of an expiry moves only when
// the suite advances clk.
func clockContext(t *testing.T) (context.Context, *Clock) {
	t.Helper()
	clk := NewClock()
	got := make(chan context.Context, 1)
	app := bites.New(bites.WithClock(clk))
	app.Service(bites.Service{Name: "bitetest-clock", Start: func(ctx context.Context) error {
		got <- ctx
		return nil
	}})
	Start(t, app)
	select {
	case ctx := <-got:
		return ctx, clk
	case <-time.After(5 * time.Second):
		t.Fatal("bitetest: the app that carries the suite's clock did not start in 5 s")
		return nil, nil
	}
}
