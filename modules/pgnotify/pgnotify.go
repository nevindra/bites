// Package pgnotify is a bites.Broker over Postgres LISTEN and NOTIFY: a bites.Topic made with
// bites.NewTopic and this Broker reaches the WebSockets of every instance of an application that
// shares one Postgres (ADR 0081).
//
// One connection of each instance does LISTEN on one channel and carries every Topic, so a Socket
// and a Topic cost no database connection. Publishing uses a pool. The package handles what an
// application gets wrong by hand: a payload over Postgres's limit is a *bites.PayloadError and never cut, a
// LISTEN connection that drops is reopened with a growing pause and each attempt logged, and a
// Topic is told when notifications may have been missed. It depends on github.com/jackc/pgx/v5.
//
// A Broker is started with Run, which blocks, and stopped with Close.
package pgnotify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nevindra/bites"
)

// MaxNotification is the largest notification Postgres accepts, in bytes: the payload must be
// shorter than 8000. The notification here is the Topic's name, a newline and the encoded message.
const MaxNotification = 7999

// maxName is the longest Topic name carried, so a name cannot take the notification's room.
const maxName = 255

// ErrClosed is returned by Publish after Close.
var ErrClosed = errors.New("pgnotify: the Broker is closed")

// Options are the settings of New.
type Options struct {
	// URL is the Postgres connection string of the LISTEN connection, and of the publishing
	// pool when Pool is nil. URL or Config is required, and not both.
	URL string
	// Config is the connection of the LISTEN connection, and of the publishing pool when Pool is
	// nil, for a program that already holds one: give pool.Config().ConnConfig to listen on the
	// database the pool reaches. A field set on a parsed config (ConnConfig.Database) is read as
	// set, where its ConnString still names what was first parsed. The Broker copies it.
	Config *pgx.ConnConfig
	// Channel is the Postgres channel every Topic is carried on. Instances that share Topics
	// must use the same one; applications that share a database must use different ones.
	// Empty means "bites". It is at most 63 bytes, a Postgres identifier.
	Channel string
	// Pool is the pool Publish uses. Nil means the Broker opens its own from URL, and Close
	// closes it; a pool given here is the caller's, and Close leaves it open.
	Pool *pgxpool.Pool
	// Log receives each lost connection and each retry. Nil means slog.Default().
	Log *slog.Logger
	// OnState is called, from the goroutine of Run, when the LISTEN connection becomes live
	// (true) and when it is lost (false). It is how an application fails its readiness while
	// the instance cannot hear: app.SetReady. It must not block.
	OnState func(listening bool)
	// MinBackoff and MaxBackoff bound the pause before a reconnect: it starts at MinBackoff,
	// doubles with each failed attempt up to MaxBackoff, and has 20% of jitter so instances
	// that lost the database together do not return together. Zero means 100ms and 10s.
	MinBackoff, MaxBackoff time.Duration
	// PublishTimeout bounds one Publish, the wait for the pool and the round trip. Zero means 5s.
	PublishTimeout time.Duration
	// KeepAlive is how often an idle LISTEN connection is checked with a query, so a connection
	// a firewall dropped without a reset is noticed. Zero means 30s.
	KeepAlive time.Duration
}

// Broker implements bites.Broker over Postgres.
type Broker struct {
	opt     Options
	pool    *pgxpool.Pool
	ownPool bool

	mu        sync.RWMutex
	subs      map[string]map[*sub]struct{}
	listening bool
	closed    bool

	running   atomic.Bool
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

type sub struct {
	deliver func([]byte)
	gap     func()
	missed  bool // notifications may have been missed since it subscribed
}

// New returns a Broker; it connects nothing until Run. It panics, naming the line that called it,
// when URL is empty or does not parse, Channel is over 63 bytes or holds a NUL, or a duration is
// negative.
func New(o Options) *Broker {
	_, file, line, _ := runtime.Caller(1)
	where := fmt.Sprintf("%s:%d", filepath.Base(file), line)
	fail := func(format string, args ...any) {
		panic(fmt.Sprintf("pgnotify.New: %s\n\tregistered at %s", fmt.Sprintf(format, args...), where))
	}
	var cfg *pgxpool.Config
	switch {
	case o.URL == "" && o.Config == nil:
		fail("URL and Config are empty: give the Postgres connection string, such as postgres://user:pass@host/db, or the *pgx.ConnConfig of the pool, pool.Config().ConnConfig")
	case o.URL != "" && o.Config != nil:
		fail("URL and Config are both given: give one, since the two could name different databases")
	case o.Config != nil:
		c, err := pgxpool.ParseConfig("")
		if err != nil {
			fail("the pool config could not be made: %v", err)
		}
		o.Config = o.Config.Copy()
		c.ConnConfig = o.Config.Copy()
		cfg = c
	default:
		c, err := pgxpool.ParseConfig(o.URL)
		if err != nil {
			fail("URL does not parse: %v", err)
		}
		o.Config = c.ConnConfig.Copy()
		cfg = c
	}
	if o.Channel == "" {
		o.Channel = "bites"
	}
	if len(o.Channel) > 63 || bytes.IndexByte([]byte(o.Channel), 0) >= 0 {
		fail("Channel %q is not a Postgres identifier: give at most 63 bytes and no NUL", o.Channel)
	}
	if o.MinBackoff < 0 || o.MaxBackoff < 0 || o.PublishTimeout < 0 || o.KeepAlive < 0 {
		fail("a duration is negative: give a positive time, or zero for the default")
	}
	if o.MinBackoff == 0 {
		o.MinBackoff = 100 * time.Millisecond
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = 10 * time.Second
	}
	if o.MaxBackoff < o.MinBackoff {
		fail("MaxBackoff %v is below MinBackoff %v: give a maximum at least the minimum", o.MaxBackoff, o.MinBackoff)
	}
	if o.PublishTimeout == 0 {
		o.PublishTimeout = 5 * time.Second
	}
	if o.KeepAlive == 0 {
		o.KeepAlive = 30 * time.Second
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	b := &Broker{opt: o, pool: o.Pool, subs: map[string]map[*sub]struct{}{}, done: make(chan struct{})}
	b.ctx, b.cancel = context.WithCancel(context.Background())
	if b.pool == nil {
		// pgxpool.NewWithConfig opens no connection: the first Publish does.
		p, err := pgxpool.NewWithConfig(context.Background(), cfg)
		if err != nil {
			fail("the publishing pool could not be made: %v", err)
		}
		b.pool, b.ownPool = p, true
	}
	return b
}

// Accepts reports whether name can be carried: not empty, at most 255 bytes, valid UTF-8, no
// newline (the separator in the notification) and no NUL.
func (b *Broker) Accepts(name string) error {
	switch {
	case name == "":
		return errors.New("the name is empty")
	case len(name) > maxName:
		return fmt.Errorf("the name is %d bytes, over %d: a notification is limited to %d bytes and the name takes its room", len(name), maxName, MaxNotification)
	case !utf8.ValidString(name):
		return errors.New("the name is not valid UTF-8")
	case bytes.ContainsAny([]byte(name), "\n\x00"):
		return errors.New(`the name has a newline or a NUL, which the notification cannot carry: use letters, digits and ".", "-", "_", ":"`)
	}
	return nil
}

// Publish sends payload to every instance listening on the channel, this one included, with
// pg_notify on the pool. The notification is the name, a newline and the payload, and it must be at
// most MaxNotification bytes: a larger one is a *bites.PayloadError whose Limit is what the name
// leaves the payload (MaxNotification less the name and the newline), and nothing is sent. It
// returns when Postgres has accepted the notification; delivery follows when the listeners read it. Notifications of one publisher that wait for each Publish arrive in order.
func (b *Broker) Publish(ctx context.Context, name string, payload []byte) error {
	if err := b.Accepts(name); err != nil {
		return fmt.Errorf("pgnotify: %w", err)
	}
	if n := len(name) + 1 + len(payload); n > MaxNotification {
		return &bites.PayloadError{Name: name, Size: len(payload), Limit: MaxNotification - len(name) - 1}
	}
	if !utf8.Valid(payload) || bytes.IndexByte(payload, 0) >= 0 {
		return fmt.Errorf("pgnotify: the payload for %q is not text Postgres can carry (invalid UTF-8 or a NUL): send JSON", name)
	}
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	ctx, cancel := context.WithTimeout(ctx, b.opt.PublishTimeout)
	defer cancel()
	if _, err := b.pool.Exec(ctx, "SELECT pg_notify($1, $2)", b.opt.Channel, name+"\n"+string(payload)); err != nil {
		return fmt.Errorf("pgnotify: NOTIFY %q: %w", name, err)
	}
	return nil
}

// Subscribe registers deliver for name. Nothing is sent to Postgres: the one LISTEN connection
// already carries every name and the Broker keeps the ones with a subscriber. A subscription
// made while the connection is down, or that the connection was lost under, is told by gap once
// the connection is back and listening.
func (b *Broker) Subscribe(name string, deliver func([]byte), gap func()) func() {
	s := &sub{deliver: deliver, gap: gap}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return func() {}
	}
	s.missed = !b.listening
	if b.subs[name] == nil {
		b.subs[name] = map[*sub]struct{}{}
	}
	b.subs[name][s] = struct{}{}
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.subs[name], s)
		if len(b.subs[name]) == 0 {
			delete(b.subs, name)
		}
		b.mu.Unlock()
	}
}

// Listening reports whether the LISTEN connection is live.
func (b *Broker) Listening() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.listening
}

// Run keeps the LISTEN connection until ctx is done or Close is called, and then returns nil. A
// connection that cannot be made or that drops is retried after a pause that grows from MinBackoff
// to MaxBackoff, each failure logged at Warn with the error and the pause, and each success at
// Info. It returns an error only when called twice. Run it on its own goroutine.
func (b *Broker) Run(ctx context.Context) error {
	if !b.running.CompareAndSwap(false, true) {
		return errors.New("pgnotify: Run is already running, or has run: one Broker has one LISTEN loop")
	}
	defer close(b.done)
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		select {
		case <-b.ctx.Done():
			stop()
		case <-ctx.Done():
		}
	}()
	pause := b.opt.MinBackoff
	for ctx.Err() == nil {
		heard, err := b.listen(ctx)
		b.setListening(false)
		if ctx.Err() != nil {
			return nil
		}
		if heard {
			pause = b.opt.MinBackoff
		}
		wait := pause + time.Duration(rand.Int64N(int64(pause)/5+1))
		b.opt.Log.Warn("pgnotify: the LISTEN connection ended", "err", err, "retry_in", wait)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		pause = min(pause*2, b.opt.MaxBackoff)
	}
	return nil
}

func (b *Broker) setListening(up bool) {
	b.mu.Lock()
	changed := b.listening != up
	b.listening = up
	var gaps []func()
	if up {
		for _, set := range b.subs {
			for s := range set {
				if s.missed {
					s.missed = false
					gaps = append(gaps, s.gap)
				}
			}
		}
	} else {
		for _, set := range b.subs {
			for s := range set {
				s.missed = true
			}
		}
	}
	b.mu.Unlock()
	if changed && b.opt.OnState != nil {
		b.opt.OnState(up)
	}
	for _, g := range gaps {
		g()
	}
}

// listen holds one connection until it fails; heard says it was listening at some point.
func (b *Broker) listen(ctx context.Context) (heard bool, err error) {
	conn, err := pgx.ConnectConfig(ctx, b.opt.Config.Copy())
	if err != nil {
		return false, err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{b.opt.Channel}.Sanitize()); err != nil {
		return false, err
	}
	b.setListening(true)
	b.opt.Log.Info("pgnotify: listening", "channel", b.opt.Channel, "database", b.opt.Config.Database, "host", b.opt.Config.Host)
	for {
		wctx, cancel := context.WithTimeout(ctx, b.opt.KeepAlive)
		n, err := conn.WaitForNotification(wctx)
		cancel()
		switch {
		case err == nil:
			b.dispatch(n.Payload)
		case ctx.Err() != nil:
			return true, ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
			// Idle for KeepAlive: a query tells a live connection from one that was dropped
			// without a reset.
			pctx, cancel := context.WithTimeout(ctx, b.opt.KeepAlive)
			_, err := conn.Exec(pctx, "SELECT 1")
			cancel()
			if err != nil {
				return true, fmt.Errorf("the keep-alive query failed: %w", err)
			}
		default:
			return true, err
		}
	}
}

// dispatch hands a notification to the subscribers of its Topic. The list is copied and the
// lock released first, because a delivery can close a slow Socket, which leaves its Topic, which
// calls the stop of a subscription.
func (b *Broker) dispatch(payload string) {
	name, data, ok := splitNotification(payload)
	if !ok {
		b.opt.Log.Warn("pgnotify: a notification on the channel is not one of ours (no topic name); another writer uses this channel", "channel", b.opt.Channel, "bytes", len(payload))
		return
	}
	b.mu.RLock()
	var list []*sub
	for s := range b.subs[name] {
		list = append(list, s)
	}
	b.mu.RUnlock()
	for _, s := range list {
		s.deliver([]byte(data))
	}
}

func splitNotification(p string) (name, data string, ok bool) {
	for i := 0; i < len(p); i++ {
		if p[i] == '\n' {
			return p[:i], p[i+1:], true
		}
	}
	return "", "", false
}

// Close stops Run, waits for it to return, ends every subscription's delivery and closes the pool
// if the Broker opened it. Publish returns ErrClosed afterwards. It is safe to call more than once.
func (b *Broker) Close() error {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		b.cancel()
		if b.running.Load() {
			<-b.done
		}
		if b.ownPool {
			b.pool.Close()
		}
	})
	return nil
}

var _ bites.Broker = (*Broker)(nil)
