package bites

import (
	"context"
	"hash/maphash"
	"net/netip"
	"strconv"
	"sync"
	"time"
	"unsafe"
)

// Rate limiting (ADR 0055): a token bucket per key, kept as one number a key (GCRA, the generic
// cell rate algorithm, which is a token bucket written as the time the bucket will next be
// full), in a bounded in-memory store by default or in a shared store the application supplies.

// RateLimitOptions configures a RateLimiter.
type RateLimitOptions struct {
	// Limit is how many requests a key may make in a Period, on average. Required.
	Limit int

	// Period is the time Limit is counted over, in whole seconds (time.Minute). Required. A key
	// earns one request back every Period/Limit.
	Period time.Duration

	// Burst is how many requests a key may make at once, the size of its bucket; Limit when zero.
	Burst int

	// Key is what the middleware limits by; the client's address when nil (Request.ClientAddr,
	// behind trusted proxies as ADR 0025 says), with an IPv6 client keyed by its /64, since one
	// host is usually given a whole /64. A Key that returns "" limits all those requests as one
	// key. Allow takes its key as an argument and does not use this.
	Key func(Request) string

	// Store is a shared store, for limits that hold across several instances of the app; the
	// in-memory store of this process when nil.
	Store RateLimitStore

	// MaxKeys bounds the in-memory store: 100,000 keys when zero, about 5 MB (50 B a key). It is refused
	// together with Store.
	MaxKeys int

	// Name is the policy's name in the RateLimit-Policy and RateLimit headers; "default" when
	// empty. At most 16 letters, digits, '-' or '_'.
	Name string
}

// RateLimitStore keeps the buckets of a RateLimiter outside the process, so several instances
// share them (Redis, Postgres). Take spends one request of key's bucket if it has one, at now,
// for a bucket of burst requests that earns one back every interval, and says what is left. It
// must be atomic for a key: two instances taking at once must not both get the last request. An
// error is the answer to the request (a 500): a store that should fail open returns Allowed with
// its error swallowed, a choice ADR 0055 leaves to the application.
type RateLimitStore interface {
	Take(ctx context.Context, key string, now time.Time, burst int, interval time.Duration) (RateLimitDecision, error)
}

// RateLimitDecision is what a RateLimitStore answers for one request.
type RateLimitDecision struct {
	// Allowed is whether the request may go on.
	Allowed bool
	// Remaining is how many more requests the key may make now.
	Remaining int
	// RetryAfter is, when the request is not allowed, how long until one is.
	RetryAfter time.Duration
	// Reset is how long until the key's bucket is full again.
	Reset time.Duration
}

// RateLimiter limits requests by key with a token bucket (ADR 0055). Its Middleware limits by the
// client's address or the Key function; its Allow is for a Provider that knows the key only once
// it has run, such as the user's id.
type RateLimiter struct {
	interval time.Duration
	burst    int
	key      func(Request) string
	store    RateLimitStore
	mem      *memoryBuckets
	name     string
	policy   string // the RateLimit-Policy value, built once
}

const (
	defaultMaxKeys = 100_000
	maxPolicyName  = 16
	// rateLimitRoom is the room serveChain adds to the slab of a request whose chain holds a
	// RateLimiter's middleware, for the RateLimit header it writes, so it costs no allocation
	// (ADR 0055, slabRoom).
	rateLimitRoom = 64
	maxBucketSpan = 10 * 365 * 24 * time.Hour
)

// NewRateLimiter returns a RateLimiter. It panics when Limit or Period is not positive, Period is
// not whole seconds or is shorter than Limit requests can be spaced (a nanosecond each), Burst is
// negative or takes more than ten years to refill, MaxKeys is negative or given with Store, or
// Name is not a short token.
func NewRateLimiter(o RateLimitOptions) *RateLimiter {
	const fn = "NewRateLimiter"
	if o.Limit <= 0 {
		refuseMiddleware(fn, "Limit is %d: give the number of requests a key may make in a Period, at least 1", o.Limit)
	}
	if o.Period < time.Second || o.Period%time.Second != 0 {
		refuseMiddleware(fn, "Period is %v, which is not a whole number of seconds: the RateLimit headers count in seconds, so give time.Second or more in whole seconds", o.Period)
	}
	if o.Burst < 0 {
		refuseMiddleware(fn, "Burst is %d: give zero for Limit, or the most requests a key may make at once", o.Burst)
	}
	if o.MaxKeys < 0 {
		refuseMiddleware(fn, "MaxKeys is %d, which is not a bound: give zero for 100,000, or a positive number", o.MaxKeys)
	}
	if o.MaxKeys > 0 && o.Store != nil {
		refuseMiddleware(fn, "MaxKeys is given with a Store: MaxKeys bounds the in-memory store, which a Store replaces, so bound the Store's own memory instead")
	}
	if o.Name == "" {
		o.Name = "default"
	}
	if len(o.Name) > maxPolicyName || !isPolicyName(o.Name) {
		refuseMiddleware(fn, "Name %q is not a policy name: use at most %d letters, digits, '-' or '_'", o.Name, maxPolicyName)
	}
	l := &RateLimiter{
		interval: o.Period / time.Duration(o.Limit),
		burst:    o.Burst,
		key:      o.Key,
		store:    o.Store,
		name:     o.Name,
		policy:   `"` + o.Name + `";q=` + strconv.Itoa(o.Limit) + ";w=" + strconv.FormatInt(int64(o.Period/time.Second), 10),
	}
	if l.burst == 0 {
		l.burst = o.Limit
	}
	if l.interval <= 0 {
		refuseMiddleware(fn, "Limit %d in a Period of %v is more than one request a nanosecond", o.Limit, o.Period)
	}
	// The bucket's whole length in time is added to a Unix time in nanoseconds; ten years keeps
	// that sum far from overflowing an int64 (292 years).
	if int64(l.burst) > int64(maxBucketSpan/l.interval) {
		refuseMiddleware(fn, "a Burst of %d at one request every %v takes more than ten years to refill: give a shorter Period or a smaller Burst", l.burst, l.interval)
	}
	if l.store == nil {
		n := o.MaxKeys
		if n == 0 {
			n = defaultMaxKeys
		}
		l.mem = newMemoryBuckets(n)
	}
	return l
}

func isPolicyName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Middleware limits every request it sees by the key of RateLimitOptions.Key, the client's address
// when that is nil. An allowed request goes on, and its answer carries RateLimit-Policy and
// RateLimit; one over the limit is answered 429 problem+json with Retry-After and the same two
// headers, and goes no further. Give it to app.Use to count every request, 404s included, or to a
// group or route for a limit of their own. With the in-memory store and the default key it
// allocates nothing for a key it has seen.
func (l *RateLimiter) Middleware() Middleware { return l.serve }

// serve is the middleware, a method value so that the API document can recognise it on a route
// (ADR 0069).
func (l *RateLimiter) serve(ctx context.Context, req Request, next Next) (Response, error) {
	var d RateLimitDecision
	var err error
	if l.key != nil {
		d, err = l.take(ctx, req, l.key(req))
	} else {
		d, err = l.takeAddr(ctx, req)
	}
	if err != nil {
		return Response{}, err
	}
	if !d.Allowed {
		return Response{}, l.refusal(d)
	}
	res := next.Call(ctx)
	res.AddHeader("RateLimit-Policy", l.policy)
	res.AddHeader("RateLimit", req.rateLimitValue(l.name, d.Remaining, d.Reset))
	return res, nil
}

// Allow spends one request of key's bucket. It returns nil when the request may go on; a 429
// *StatusError carrying Retry-After, RateLimit-Policy and RateLimit when it may not; and the
// store's error when the store failed. ctx is the request's, or one derived from it, so the bucket
// is measured on the app's clock (Now). It is for code that learns the key as it runs: the Provider
// of the authenticated user calls it with the user's id and returns its error, which is how a limit
// per user is written, since middleware runs before any Provider (ADR 0048), and a handler calls it
// with a key it read from the body (the email of a sign-in) and returns its error. The API document
// cannot see a call inside a Provider or a handler: give the routes ErrorStatuses(429) to list the
// 429.
func (l *RateLimiter) Allow(ctx context.Context, key string) error {
	d, err := l.takeAt(ctx, Now(ctx), key)
	if err != nil {
		return err
	}
	if !d.Allowed {
		return l.refusal(d)
	}
	return nil
}

func (l *RateLimiter) refusal(d RateLimitDecision) error {
	retry := ceilSeconds(d.RetryAfter)
	return TooManyRequests("the rate limit is reached; retry after "+strconv.FormatInt(retry, 10)+" s").
		WithHeader("Retry-After", strconv.FormatInt(retry, 10)).
		WithHeader("RateLimit-Policy", l.policy).
		WithHeader("RateLimit", `"`+l.name+`";r=0;t=`+strconv.FormatInt(retry, 10))
}

func (l *RateLimiter) now(req Request) time.Time {
	if req.live() {
		return req.run.a.clk.Now()
	}
	return time.Now()
}

// take spends one request of a key given as a string.
func (l *RateLimiter) take(ctx context.Context, req Request, key string) (RateLimitDecision, error) {
	return l.takeAt(ctx, l.now(req), key)
}

func (l *RateLimiter) takeAt(ctx context.Context, now time.Time, key string) (RateLimitDecision, error) {
	if l.mem != nil {
		return l.mem.take(unsafe.Slice(unsafe.StringData(key), len(key)), now, l.burst, l.interval), nil
	}
	return l.store.Take(ctx, key, now, l.burst, l.interval)
}

// takeAddr spends one request of the client address's bucket, the key built on the stack: a tag
// byte, then 4 bytes for IPv4 or the 8 bytes of the /64 for IPv6.
func (l *RateLimiter) takeAddr(ctx context.Context, req Request) (RateLimitDecision, error) {
	a := req.ClientAddr()
	// A tag byte no UTF-8 text starts with (0xfe, 0xff) keeps an address from sharing a bucket with a key Allow or
	// a Key function gave the same limiter.
	var buf [9]byte
	var k []byte
	if a.Is4() {
		b := a.As4()
		k = append(append(buf[:0], 0xfe), b[:]...)
	} else {
		b := a.As16()
		k = append(append(buf[:0], 0xff), b[:8]...)
	}
	now := l.now(req)
	if l.mem != nil {
		return l.mem.take(k, now, l.burst, l.interval), nil
	}
	key := a.String()
	if a.Is6() {
		key = netip.PrefixFrom(a, 64).Masked().String()
	}
	return l.store.Take(ctx, key, now, l.burst, l.interval)
}

// rateLimitValue writes `"name";r=remaining;t=reset` in the request's slab, which serveChain made
// room for (rateLimitRoom, slabRoom), so the header costs no allocation of its own.
func (r Request) rateLimitValue(name string, remaining int, reset time.Duration) string {
	var tmp [rateLimitRoom + 16]byte
	b := append(tmp[:0], '"')
	b = append(b, name...)
	b = append(b, `";r=`...)
	b = strconv.AppendInt(b, int64(remaining), 10)
	b = append(b, ";t="...)
	b = strconv.AppendInt(b, ceilSeconds(reset), 10)
	if !r.live() {
		return string(b)
	}
	dst := r.run.ex.Slab().Reserve(len(b))
	copy(dst, b)
	return unsafe.String(&dst[0], len(dst))
}

func ceilSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Second - 1) / time.Second)
}

// memoryBuckets is the in-memory store: shards of a map from key to the time the key's bucket is
// next full (its "theoretical arrival time"). A key whose time has passed has a full bucket and
// carries nothing a fresh key would not, which is what lets eviction drop it exactly.
type memoryBuckets struct {
	seed   maphash.Seed
	shards [memShards]memShard
	cap    int // keys a shard holds before it evicts
}

const memShards = 64

type memShard struct {
	mu sync.Mutex
	m  map[string]*int64
	_  [40]byte // keeps two shards' locks off one cache line
}

func newMemoryBuckets(maxKeys int) *memoryBuckets {
	b := &memoryBuckets{seed: maphash.MakeSeed(), cap: max(1, maxKeys/memShards)}
	for i := range b.shards {
		b.shards[i].m = make(map[string]*int64)
	}
	return b
}

// take is GCRA: a request is allowed when, after adding one interval to the later of now and the
// key's time, the key's time is at most burst intervals ahead of now.
func (b *memoryBuckets) take(key []byte, now time.Time, burst int, interval time.Duration) RateLimitDecision {
	sh := &b.shards[maphash.Bytes(b.seed, key)%memShards]
	t := now.UnixNano()
	limit := int64(burst) * int64(interval)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	cell := sh.m[string(key)] // a lookup by a converted key allocates nothing
	tat := t
	if cell != nil && *cell > t {
		tat = *cell
	}
	next := tat + int64(interval)
	if next-t > limit {
		return RateLimitDecision{RetryAfter: time.Duration(next - t - limit), Reset: time.Duration(tat - t)}
	}
	if cell == nil {
		if len(sh.m) >= b.cap {
			sh.evict(t, b.cap)
		}
		cell = new(int64)
		sh.m[string(key)] = cell
	}
	*cell = next
	return RateLimitDecision{Allowed: true, Remaining: int((limit - (next - t)) / int64(interval)), Reset: time.Duration(next - t)}
}

// evict makes room in a full shard. It drops every key whose bucket is full again, which loses
// nothing; when that frees less than an eighth of the shard (a client rotating keys faster than
// they refill), it also drops keys in the map's own random order until an eighth is free. A sweep
// is one pass over the shard and frees at least an eighth of it, so it runs at most once every
// cap/8 new keys: a constant cost a new key, amortized.
func (sh *memShard) evict(now int64, cap int) {
	want := max(1, cap/8)
	freed := 0
	for k, c := range sh.m {
		if *c <= now {
			delete(sh.m, k)
			freed++
		}
	}
	for k := range sh.m {
		if freed >= want {
			break
		}
		delete(sh.m, k)
		freed++
	}
}
