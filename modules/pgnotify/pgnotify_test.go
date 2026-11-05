package pgnotify_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/pgnotify"
)

// pgURL is the Postgres the integration tests use. They are skipped without it, with a message
// naming the variable, so a run with no database says what it left out.
func pgURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("BITES_PG_URL")
	if u == "" {
		t.Skip("BITES_PG_URL names no Postgres, so this test that needs one is skipped; for example BITES_PG_URL=postgres://postgres:pg@127.0.0.1:55433/postgres")
	}
	return u
}

// logBuf is a log destination safe for the goroutines of the tests.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }
func (l *logBuf) logger() *slog.Logger        { return slog.New(slog.NewTextHandler(l, nil)) }

var chanSeq atomic.Int32

// newBroker returns a started Broker on a channel of its own to the test, so tests that share a
// database do not hear each other. A second Broker for the same instance set passes the channel.
func newBroker(t *testing.T, url, channel string, o pgnotify.Options) *pgnotify.Broker {
	t.Helper()
	o.URL, o.Channel = url, channel
	if o.MinBackoff == 0 {
		o.MinBackoff, o.MaxBackoff = 20*time.Millisecond, 100*time.Millisecond
	}
	b := pgnotify.New(o)
	go b.Run(context.Background())
	t.Cleanup(func() { b.Close() })
	waitFor(t, "the broker to listen", b.Listening)
	return b
}

func channelName(t *testing.T) string { return fmt.Sprintf("t%d_%d", os.Getpid(), chanSeq.Add(1)) }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- no database needed

func TestAPublishOverThePostgresLimitIsAnErrorNamingTheLimitAndTheSizeAndSendsNothing(t *testing.T) {
	// The pool is lazy, so this reaches no database: the size is checked before the pool is used.
	b := pgnotify.New(pgnotify.Options{URL: "postgres://nobody@127.0.0.1:1/none"})
	defer b.Close()
	payload := bytes.Repeat([]byte("x"), 8000-len("room.1")-1) // the notification is 8000 bytes
	// room.1 and its newline leave 7999 - 7 = 7992 bytes: the payload above is one more.
	var pe *bites.PayloadError
	err := b.Publish(context.Background(), "room.1", payload)
	if !errors.As(err, &pe) || pe.Name != "room.1" || pe.Size != len(payload) || pe.Limit != pgnotify.MaxNotification-len("room.1")-1 {
		t.Fatalf("a notification of 8000 bytes returned %#v, want a *bites.PayloadError whose Limit is what the name leaves", err)
	}
	err = b.Publish(context.Background(), "room.1", append(payload, 'x'))
	if !errors.As(err, &pe) || pe.Size != len(payload)+1 {
		t.Fatalf("a notification of 8001 bytes returned %v", err)
	}
}

func TestAPayloadPostgresCannotCarryIsRefusedBeforeItIsSent(t *testing.T) {
	b := pgnotify.New(pgnotify.Options{URL: "postgres://nobody@127.0.0.1:1/none"})
	defer b.Close()
	for _, p := range [][]byte{{'a', 0, 'b'}, {0xff, 0xfe}} {
		if err := b.Publish(context.Background(), "t", p); err == nil || !strings.Contains(err.Error(), "send JSON") {
			t.Errorf("payload %q returned %v", p, err)
		}
	}
}

func TestANameTheBrokerCannotCarryIsNotAccepted(t *testing.T) {
	b := pgnotify.New(pgnotify.Options{URL: "postgres://nobody@127.0.0.1:1/none"})
	defer b.Close()
	for name, want := range map[string]string{
		"":                       "the name is empty",
		"a\nb":                   "newline",
		strings.Repeat("n", 256): "256 bytes, over 255",
		"bad\xffname":            "not valid UTF-8",
	} {
		if err := b.Accepts(name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Accepts(%q) = %v, want it to say %q", name, err, want)
		}
	}
	if err := b.Accepts("room.7"); err != nil {
		t.Errorf("Accepts(room.7) = %v", err)
	}
}

func TestNewRefusesAnOptionThatCannotWorkNamingTheLine(t *testing.T) {
	for name, tc := range map[string]struct {
		o    pgnotify.Options
		want string
	}{
		"no url":         {pgnotify.Options{}, "pgnotify.New: URL and Config are empty: give the Postgres connection string"},
		"url and config": {pgnotify.Options{URL: "postgres://h/d", Config: mustConfig(t, "postgres://h/e")}, "URL and Config are both given: give one"},
		"long channel":   {pgnotify.Options{URL: "postgres://h/d", Channel: strings.Repeat("c", 64)}, "is not a Postgres identifier: give at most 63 bytes"},
		"backoff":        {pgnotify.Options{URL: "postgres://h/d", MinBackoff: time.Second, MaxBackoff: time.Millisecond}, "MaxBackoff 1ms is below MinBackoff 1s"},
		"negative":       {pgnotify.Options{URL: "postgres://h/d", KeepAlive: -1}, "a duration is negative"},
	} {
		func() {
			defer func() {
				r, _ := recover().(string)
				if !strings.Contains(r, tc.want) || !strings.Contains(r, "\n\tregistered at pgnotify_test.go:") {
					t.Errorf("%s: panic %q, want %q and the line", name, r, tc.want)
				}
			}()
			pgnotify.New(tc.o)
		}()
	}
}

func TestACoreTopicRefusesABrokerNameItDoesNotCarry(t *testing.T) {
	b := pgnotify.New(pgnotify.Options{URL: "postgres://nobody@127.0.0.1:1/none"})
	defer b.Close()
	defer func() {
		r := recover()
		if r == nil || !strings.Contains(fmt.Sprint(r), "the broker does not carry this name: the name has a newline") {
			t.Fatalf("panic %v", r)
		}
	}()
	bites.NewTopic[line]("a\nb", b, bites.TopicOptions{})
}

func TestAModuleImportsOnlyTheExportedCore(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Skip(err)
	}
	// The core's packages the module reaches are listed as dependencies; only its own internal
	// imports would be a fault, and Go would allow them by path (ADR 0070).
	src, _ := os.ReadFile("pgnotify.go")
	if strings.Contains(string(src), "bites/internal") {
		t.Errorf("pgnotify.go imports the core's internal packages:\n%s", out)
	}
}

// ---- against a real Postgres

type line struct {
	Text string `json:"text"`
}

func TestARealPostgresAcceptsANotificationOf7999BytesAndRefusesOneOf8000(t *testing.T) {
	url := pgURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "SELECT pg_notify('bites_limit', $1)", strings.Repeat("x", pgnotify.MaxNotification)); err != nil {
		t.Fatalf("Postgres refused %d bytes, so MaxNotification is too high: %v", pgnotify.MaxNotification, err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_notify('bites_limit', $1)", strings.Repeat("x", pgnotify.MaxNotification+1)); err == nil {
		t.Fatalf("Postgres accepted %d bytes, so MaxNotification is too low", pgnotify.MaxNotification+1)
	}
	// And through the Broker, which sends the 7999 and refuses the 8000 itself.
	b := newBroker(t, url, channelName(t), pgnotify.Options{})
	got := make(chan int, 1)
	b.Subscribe("n", func(p []byte) { got <- len(p) }, func() {})
	if err := b.Publish(ctx, "n", bytes.Repeat([]byte("x"), pgnotify.MaxNotification-2)); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-got:
		if n != pgnotify.MaxNotification-2 {
			t.Fatalf("delivered %d bytes", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the largest notification never arrived")
	}
	if err := b.Publish(ctx, "n", bytes.Repeat([]byte("x"), pgnotify.MaxNotification-1)); err == nil {
		t.Fatal("8000 bytes were sent")
	}
}

func TestAPublishOnOneInstanceReachesBothInstancesInOrderAndTheOwnOnceEach(t *testing.T) {
	url, ch := pgURL(t), channelName(t)
	a, b := newBroker(t, url, ch, pgnotify.Options{}), newBroker(t, url, ch, pgnotify.Options{})
	var mu sync.Mutex
	var gotA, gotB []string
	a.Subscribe("room.1", func(p []byte) { mu.Lock(); gotA = append(gotA, string(p)); mu.Unlock() }, func() {})
	b.Subscribe("room.1", func(p []byte) { mu.Lock(); gotB = append(gotB, string(p)); mu.Unlock() }, func() {})
	b.Subscribe("room.2", func(p []byte) { t.Errorf("room.2 heard %s", p) }, func() {})
	const n = 50
	for i := range n {
		if err := a.Publish(context.Background(), "room.1", []byte(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "both to hear all", func() bool { mu.Lock(); defer mu.Unlock(); return len(gotA) == n && len(gotB) == n })
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for i := range n {
		if gotA[i] != fmt.Sprint(i) || gotB[i] != fmt.Sprint(i) {
			t.Fatalf("out of order or doubled at %d: A %v B %v", i, gotA, gotB)
		}
	}
	if len(gotA) != n || len(gotB) != n {
		t.Fatalf("A heard %d, B heard %d, want %d each", len(gotA), len(gotB), n)
	}
}

func TestTwoInstancesPublishingAtOnceAreHeardInTheSameOrderByBoth(t *testing.T) {
	url, ch := pgURL(t), channelName(t)
	a, b := newBroker(t, url, ch, pgnotify.Options{}), newBroker(t, url, ch, pgnotify.Options{})
	var mu sync.Mutex
	var gotA, gotB []string
	a.Subscribe("t", func(p []byte) { mu.Lock(); gotA = append(gotA, string(p)); mu.Unlock() }, func() {})
	b.Subscribe("t", func(p []byte) { mu.Lock(); gotB = append(gotB, string(p)); mu.Unlock() }, func() {})
	var wg sync.WaitGroup
	for _, br := range []struct {
		b *pgnotify.Broker
		p string
	}{{a, "a"}, {b, "b"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 40 {
				br.b.Publish(context.Background(), "t", []byte(fmt.Sprint(br.p, i)))
			}
		}()
	}
	wg.Wait()
	waitFor(t, "both to hear 80", func() bool { mu.Lock(); defer mu.Unlock(); return len(gotA) == 80 && len(gotB) == 80 })
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(gotA, ",") != strings.Join(gotB, ",") {
		t.Fatalf("the instances heard different orders:\nA %v\nB %v", gotA, gotB)
	}
}

func TestALostListenConnectionIsReopenedLoggedAndSignalsAGapAndMessagesFlowAgain(t *testing.T) {
	url, ch := pgURL(t), channelName(t)
	logs := &logBuf{}
	var states []bool
	var smu sync.Mutex
	b := newBroker(t, url, ch, pgnotify.Options{Log: logs.logger(), OnState: func(up bool) { smu.Lock(); states = append(states, up); smu.Unlock() }})
	pub := pgnotify.New(pgnotify.Options{URL: url, Channel: ch}) // publishes only: never Run, so it holds no LISTEN
	defer pub.Close()
	var gaps atomic.Int32
	got := make(chan string, 4)
	b.Subscribe("t", func(p []byte) { got <- string(p) }, func() { gaps.Add(1) })

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	// Only the LISTEN connection of this test has a LISTEN on its channel.
	var killed int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM (SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE 'LISTEN "`+ch+`"%' AND pid <> pg_backend_pid()) k`).Scan(&killed); err != nil || killed != 1 {
		t.Fatalf("killed %d listen connections, err %v", killed, err)
	}
	waitFor(t, "the connection to drop and come back", func() bool {
		smu.Lock()
		defer smu.Unlock()
		return len(states) >= 3 && states[len(states)-1]
	})
	waitFor(t, "the gap", func() bool { return gaps.Load() == 1 })
	if err := pub.Publish(ctx, "t", []byte("after")); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m != "after" {
			t.Fatalf("heard %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing is delivered after the reconnect")
	}
	if !strings.Contains(logs.String(), "the LISTEN connection ended") || !strings.Contains(logs.String(), "retry_in") {
		t.Fatalf("the lost connection was not logged with its retry: %s", logs.String())
	}
	smu.Lock()
	defer smu.Unlock()
	if fmt.Sprint(states) != "[true false true]" {
		t.Fatalf("states %v, want [true false true]", states)
	}
}

func TestATopicOnTheBrokerTellsItsSocketsToReloadAfterAGapAndDeliversAcrossInstances(t *testing.T) {
	url, ch := pgURL(t), channelName(t)
	type frame struct {
		Kind string `json:"kind"`
		Text string `json:"text,omitempty"`
	}
	br := [2]*pgnotify.Broker{newBroker(t, url, ch, pgnotify.Options{}), newBroker(t, url, ch, pgnotify.Options{})}
	var topics [2]*bites.Topic[frame]
	var clients [2]*bitetest.WS
	for i := range topics {
		topics[i] = bites.NewTopic[frame]("room.1", br[i], bites.TopicOptions{OnGap: func() { topics[i].PublishLocal(frame{Kind: "reload"}) }})
		app := bites.New()
		app.WebSocket("/ws", func(_ context.Context, _ struct{}, s *bites.Socket[frame]) (bites.Inbox[struct{}], error) {
			topics[i].Subscribe(s)
			return bites.Inbox[struct{}]{}, nil
		})
		clients[i] = bitetest.Start(t, app).DialWS(bitetest.WSRequest{Target: "/ws"})
	}
	waitFor(t, "both sockets to join", func() bool { return topics[0].Len() == 1 && topics[1].Len() == 1 })
	if err := topics[0].Publish(frame{Kind: "message", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range clients {
		if got := c.ReadText(); got != `{"kind":"message","text":"hi"}` {
			t.Fatalf("got %s", got)
		}
	}
	// Instance 1 loses its connection.
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE 'LISTEN "`+ch+`"%' AND pid <> pg_backend_pid()`); err != nil {
		t.Fatal(err)
	}
	// Both lost theirs (they share the channel): each socket is told to reload, once.
	for _, c := range clients {
		if got := c.ReadText(); got != `{"kind":"reload"}` {
			t.Fatalf("got %s, want the reload notice", got)
		}
	}
}

func TestACloseStopsRunAndPublishAfterItReturnsErrClosed(t *testing.T) {
	url := pgURL(t)
	b := pgnotify.New(pgnotify.Options{URL: url, Channel: channelName(t)})
	ran := make(chan error, 1)
	go func() { ran <- b.Run(context.Background()) }()
	waitFor(t, "listening", b.Listening)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-ran; err != nil {
		t.Fatalf("Run returned %v after Close", err)
	}
	if b.Listening() {
		t.Fatal("still listening after Close")
	}
	if err := b.Publish(context.Background(), "t", []byte("x")); err != pgnotify.ErrClosed {
		t.Fatalf("Publish returned %v, want ErrClosed", err)
	}
	if err := b.Run(context.Background()); err == nil {
		t.Fatal("a second Run returned nil")
	}
}

func TestAnIdleConnectionSurvivesManyKeepAliveChecksAndStillDelivers(t *testing.T) {
	url, ch := pgURL(t), channelName(t)
	logs := &logBuf{}
	b := newBroker(t, url, ch, pgnotify.Options{KeepAlive: 30 * time.Millisecond, Log: logs.logger()})
	got := make(chan string, 1)
	b.Subscribe("t", func(p []byte) { got <- string(p) }, func() { t.Error("a keep-alive check signalled a gap") })
	time.Sleep(400 * time.Millisecond) // more than ten checks
	if err := b.Publish(context.Background(), "t", []byte("still")); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m != "still" {
			t.Fatalf("heard %q", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing delivered after the keep-alive checks")
	}
	if strings.Contains(logs.String(), "ended") {
		t.Fatalf("the connection was reopened by a keep-alive check: %s", logs.String())
	}
}

func mustConfig(t *testing.T, url string) *pgx.ConnConfig {
	t.Helper()
	c, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestABrokerGivenAConfigListensOnTheDatabaseItNamesNotTheOneItsConnStringNames(t *testing.T) {
	url := pgURL(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Skip(err)
	}
	defer admin.Close(ctx)
	db := fmt.Sprintf("pgnotify_cfg_%d", os.Getpid())
	admin.Exec(ctx, "DROP DATABASE IF EXISTS "+db)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)") })

	cfg := mustConfig(t, url) // ConnString names the admin database
	cfg.Database = db         // set as a test helper does after copying a template
	ch := channelName(t)
	var logs logBuf
	b := pgnotify.New(pgnotify.Options{Config: cfg, Channel: ch, Log: logs.logger(), MinBackoff: 20 * time.Millisecond, MaxBackoff: 100 * time.Millisecond})
	go b.Run(ctx)
	t.Cleanup(func() { b.Close() })
	waitFor(t, "the broker to listen", b.Listening)
	got := make(chan string, 4)
	defer b.Subscribe("room", func(p []byte) { got <- string(p) }, func() {})()

	notify := func(c *pgx.ConnConfig, payload string) {
		conn, err := pgx.ConnectConfig(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, "SELECT pg_notify($1, $2)", ch, "room\n"+payload); err != nil {
			t.Fatal(err)
		}
	}
	notify(mustConfig(t, url), "on the admin database")
	notify(cfg, "on the configured database")
	select {
	case p := <-got:
		if p != "on the configured database" {
			t.Fatalf("the broker heard %q: it listens on the database its ConnString names", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was heard on the configured database")
	}
	if !strings.Contains(logs.String(), "database="+db) {
		t.Fatalf("the listening line does not name the database:\n%s", logs.String())
	}
}

func TestTheBrokerKeepsTheContractOfEveryBroker(t *testing.T) {
	url := pgURL(t)
	const name = "conformance.topic"
	bitetest.TestBroker(t, bitetest.BrokerSuite{
		New: func(t *testing.T) (bites.Broker, bites.Broker) {
			ch := channelName(t)
			return newBroker(t, url, ch, pgnotify.Options{}), newBroker(t, url, ch, pgnotify.Options{})
		},
		Name:       name,
		Refused:    []string{"", "a\nb", strings.Repeat("n", 256), "bad\xffname"},
		MaxPayload: pgnotify.MaxNotification - len(name) - 1,
	})
}
