package pgsession_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/pgsession"
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

var tableSeq atomic.Int32

// newStore returns a Store on a table of its own to the test, created now and dropped at the end,
// so tests that share a database run in parallel without hearing each other.
func newStore(t *testing.T, o pgsession.Options) (*pgsession.Store, *pgxpool.Pool) {
	t.Helper()
	url := pgURL(t)
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	o.Pool = pool
	o.Table = fmt.Sprintf("t_sess_%d_%d", os.Getpid(), tableSeq.Add(1))
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	st := pgsession.New(o)
	if err := st.CreateTable(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DROP TABLE IF EXISTS "`+o.Table+`"`) })
	return st, pool
}

func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM "`+table+`"`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var ctx = context.Background()

func TestASavedSessionIsLoadedWithItsData(t *testing.T) {
	t.Parallel()
	st, _ := newStore(t, pgsession.Options{})
	if err := st.Save(ctx, "id-1", []byte(`{"a":1}`), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	data, found, err := st.Load(ctx, "id-1")
	if err != nil || !found || string(data) != `{"a":1}` {
		t.Fatalf("Load = %q, %v, %v", data, found, err)
	}
	if _, found, _ := st.Load(ctx, "other"); found {
		t.Fatal("an id that was never saved was found")
	}
	if err := st.Save(ctx, "id-1", []byte("v2"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if data, _, _ := st.Load(ctx, "id-1"); string(data) != "v2" {
		t.Fatalf("a second Save did not replace the data: %q", data)
	}
}

func TestAnExpiredSessionIsNotLoaded(t *testing.T) {
	t.Parallel()
	st, _ := newStore(t, pgsession.Options{})
	if err := st.Save(ctx, "old", []byte("x"), time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := st.Load(ctx, "old"); found || err != nil {
		t.Fatalf("an expired row loaded: found=%v err=%v", found, err)
	}
}

func TestDeletingASessionThatIsNotThereIsNotAnError(t *testing.T) {
	t.Parallel()
	st, _ := newStore(t, pgsession.Options{})
	if err := st.Delete(ctx, "nothing"); err != nil {
		t.Fatalf("Delete of nothing: %v", err)
	}
	st.Save(ctx, "a", []byte("x"), time.Now().Add(time.Hour))
	if err := st.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := st.Load(ctx, "a"); found {
		t.Fatal("a deleted session loaded")
	}
}

func TestSaveOwnedReplacesTheOwnerTheIdHad(t *testing.T) {
	t.Parallel()
	st, _ := newStore(t, pgsession.Options{})
	exp := time.Now().Add(time.Hour)
	st.SaveOwned(ctx, "s", "ana", []byte("1"), exp)
	st.SaveOwned(ctx, "s", "budi", []byte("2"), exp)
	if n, err := st.DeleteOwner(ctx, "ana", ""); n != 0 || err != nil {
		t.Fatalf("the old owner still found it: %d, %v", n, err)
	}
	if n, err := st.DeleteOwner(ctx, "budi", ""); n != 1 || err != nil {
		t.Fatalf("the new owner did not find it: %d, %v", n, err)
	}
	st.SaveOwned(ctx, "s", "ana", []byte("1"), exp)
	st.Save(ctx, "s", []byte("3"), exp)
	if n, _ := st.DeleteOwner(ctx, "ana", ""); n != 0 {
		t.Fatal("a plain Save left the owner on the session")
	}
}

func TestDeleteOwnerDeletesOnlyThatOwnersSessionsAndReturnsTheCount(t *testing.T) {
	t.Parallel()
	st, _ := newStore(t, pgsession.Options{})
	exp := time.Now().Add(time.Hour)
	for _, id := range []string{"a1", "a2", "a3"} {
		st.SaveOwned(ctx, id, "ana", []byte(id), exp)
	}
	st.SaveOwned(ctx, "b1", "budi", []byte("b1"), exp)
	n, err := st.DeleteOwner(ctx, "ana", "")
	if err != nil || n != 3 {
		t.Fatalf("DeleteOwner = %d, %v; want 3", n, err)
	}
	for _, id := range []string{"a1", "a2", "a3"} {
		if _, found, _ := st.Load(ctx, id); found {
			t.Fatalf("%s survived", id)
		}
	}
	if _, found, _ := st.Load(ctx, "b1"); !found {
		t.Fatal("another owner's session was deleted")
	}
	if n, err := st.DeleteOwner(ctx, "ana", ""); n != 0 || err != nil {
		t.Fatalf("an owner with nothing = %d, %v; want 0, nil", n, err)
	}
}

func TestDeleteOwnerKeepsTheSessionItIsToldToKeep(t *testing.T) {
	t.Parallel()
	st, _ := newStore(t, pgsession.Options{})
	exp := time.Now().Add(time.Hour)
	for _, id := range []string{"a1", "a2", "a3"} {
		st.SaveOwned(ctx, id, "ana", []byte(id), exp)
	}
	if n, err := st.DeleteOwner(ctx, "ana", "a2"); n != 2 || err != nil {
		t.Fatalf("DeleteOwner keeping a2 = %d, %v; want 2", n, err)
	}
	if _, found, _ := st.Load(ctx, "a2"); !found {
		t.Fatal("the session to keep was deleted")
	}
	if _, found, _ := st.Load(ctx, "a1"); found {
		t.Fatal("a session not kept survived")
	}
}

func TestAnEmptyOwnerIsNeverFoundByDeleteOwner(t *testing.T) {
	t.Parallel()
	st, _ := newStore(t, pgsession.Options{})
	exp := time.Now().Add(time.Hour)
	st.SaveOwned(ctx, "anon1", "", []byte("x"), exp)
	st.Save(ctx, "anon2", []byte("y"), exp)
	if n, err := st.DeleteOwner(ctx, "", ""); n != 0 || err != nil {
		t.Fatalf("DeleteOwner(\"\") = %d, %v", n, err)
	}
	for _, id := range []string{"anon1", "anon2"} {
		if _, found, _ := st.Load(ctx, id); !found {
			t.Fatalf("%s, an anonymous session, was deleted", id)
		}
	}
}

func TestTheSweepDeletesExpiredRowsAndKeepsLiveOnes(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgsession.Options{})
	for i := range 2500 {
		st.SaveOwned(ctx, fmt.Sprint("dead", i), "ana", []byte("x"), time.Now().Add(-time.Minute))
	}
	st.Save(ctx, "live1", []byte("x"), time.Now().Add(time.Hour))
	st.SaveOwned(ctx, "live2", "ana", []byte("x"), time.Now().Add(time.Hour))
	n, err := st.Sweep(ctx)
	if err != nil || n != 2500 {
		t.Fatalf("Sweep = %d, %v; want 2500 (more than one batch)", n, err)
	}
	var live int
	pool.QueryRow(ctx, `SELECT count(*) FROM `+tableOf(st)).Scan(&live)
	if live != 2 {
		t.Fatalf("%d rows left; want the 2 live ones", live)
	}
}

// tableOf reads the quoted table name out of the Schema, so a test needs no second name for it.
func tableOf(st *pgsession.Store) string {
	s := st.Schema()
	s = s[strings.Index(s, `"`):]
	return s[:strings.Index(s[1:], `"`)+2]
}

func TestTheServiceSweepsOnItsInterval(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgsession.Options{SweepEvery: 20 * time.Millisecond})
	st.Save(ctx, "dead", []byte("x"), time.Now().Add(-time.Minute))
	svc := st.Service()
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(ctx); err == nil || !strings.Contains(err.Error(), "one Service per Store") {
		t.Fatalf("a second Start = %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var n int
		pool.QueryRow(ctx, `SELECT count(*) FROM `+tableOf(st)).Scan(&n)
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the service did not sweep the expired row")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := svc.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Stop(ctx); err != nil {
		t.Fatalf("a second Stop: %v", err)
	}
}

func TestSweepsOnSeveralInstancesAtOnceDeleteEachRowOnce(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgsession.Options{})
	for i := range 3000 {
		st.Save(ctx, fmt.Sprint("dead", i), []byte("x"), time.Now().Add(-time.Minute))
	}
	var wg sync.WaitGroup
	var total atomic.Int32
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := pgsession.New(pgsession.Options{Pool: pool, Table: tableName(st)}).Sweep(ctx)
			if err != nil {
				t.Error(err)
			}
			total.Add(int32(n))
		}()
	}
	wg.Wait()
	if total.Load() != 3000 {
		t.Fatalf("the sweeps deleted %d rows between them; want 3000", total.Load())
	}
}

func tableName(st *pgsession.Store) string { return strings.Trim(tableOf(st), `"`) }

func TestTheSessionIdIsNotStoredInTheClear(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgsession.Options{})
	st.Save(ctx, "secret-session-id", []byte("payload"), time.Now().Add(time.Hour))
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM `+tableOf(st)+` WHERE id_hash = $1`, []byte("secret-session-id")).Scan(&n)
	if n != 0 {
		t.Fatal("the raw id is a key in the table")
	}
	var id []byte
	pool.QueryRow(ctx, `SELECT id_hash FROM `+tableOf(st)).Scan(&id)
	if len(id) != 32 || bytes.Contains(id, []byte("secret")) {
		t.Fatalf("the key is %x, not a SHA-256", id)
	}
}

func TestCreateTableCanRunTwiceAndSchemaNamesTheTableAndItsIndexes(t *testing.T) {
	t.Parallel()
	st, _ := newStore(t, pgsession.Options{})
	if err := st.CreateTable(ctx); err != nil {
		t.Fatalf("a second CreateTable: %v", err)
	}
	for _, want := range []string{"CREATE TABLE IF NOT EXISTS", "id_hash bytea PRIMARY KEY", "_owner_idx", "WHERE owner IS NOT NULL", "_expires_idx"} {
		if !strings.Contains(st.Schema(), want) {
			t.Errorf("Schema lacks %q:\n%s", want, st.Schema())
		}
	}
}

func TestAStoreErrorIsWrappedWithTheOperation(t *testing.T) {
	t.Parallel()
	pool, err := pgxpool.New(ctx, pgURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	st := pgsession.New(pgsession.Options{Pool: pool, Table: "t_sess_never_created"})
	_, _, err = st.Load(ctx, "x")
	if err == nil || !strings.HasPrefix(err.Error(), "pgsession: load: ") {
		t.Fatalf("Load on a missing table = %v", err)
	}
	if err := st.Save(ctx, "x", nil, time.Now()); err == nil || !strings.HasPrefix(err.Error(), "pgsession: save: ") {
		t.Fatalf("Save = %v", err)
	}
	if _, err := st.DeleteOwner(ctx, "o", ""); err == nil || !strings.HasPrefix(err.Error(), "pgsession: delete owner: ") {
		t.Fatalf("DeleteOwner = %v", err)
	}
}

func TestNewRefusesABadOptionAndSaysWhatToChange(t *testing.T) {
	t.Parallel()
	pool, err := pgxpool.New(ctx, "postgres://u@127.0.0.1:1/none") // lazy: nothing connects
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, c := range []struct {
		name string
		o    pgsession.Options
		want string
	}{
		{"a nil pool", pgsession.Options{}, "Pool is nil: give the application's *pgxpool.Pool"},
		{"a table with a dash", pgsession.Options{Pool: pool, Table: "my-sessions"}, `Table "my-sessions" is not a plain identifier`},
		{"a table with a quote", pgsession.Options{Pool: pool, Table: `a"; drop table x; --`}, "is not a plain identifier"},
		{"a table that starts with a digit", pgsession.Options{Pool: pool, Table: "1st"}, "is not a plain identifier"},
		{"a table over 50 bytes", pgsession.Options{Pool: pool, Table: strings.Repeat("a", 51)}, "at most 50 bytes"},
		{"a negative sweep", pgsession.Options{Pool: pool, SweepEvery: -time.Second}, "SweepEvery is -1s, which is not a time: give a positive duration, or zero for 10m0s"},
	} {
		func() {
			defer func() {
				msg, _ := recover().(string)
				if !strings.Contains(msg, c.want) || !strings.Contains(msg, "registered at pgsession_test.go:") {
					t.Errorf("%s: panic = %q; want it to contain %q and the caller's line", c.name, msg, c.want)
				}
			}()
			pgsession.New(c.o)
			t.Errorf("%s: New did not panic", c.name)
		}()
	}
}

// Login is a session type that states its owner.
type Login struct {
	UserID string `json:"user_id"`
}

func (l Login) SessionOwner() string { return l.UserID }

func TestARevokedOwnerIsSignedOutOnTheNextRequestThroughBites(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgsession.Options{})
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	logins := bites.NewSessions[Login](bites.SessionOptions{Keys: [][]byte{bytes.Repeat([]byte{7}, bites.SessionKeyLen)}, Store: st})
	app.Provide(logins.Load)
	app.Post("/login/{user}", func(ctx context.Context, in struct {
		S    bites.Session[Login]
		Path struct{ User string }
	}) (bites.NoContent, error) {
		c, err := in.S.Renew(ctx, Login{UserID: in.Path.User})
		return bites.NoContent{Cookies: []bites.Cookie{c}}, err
	})
	app.Get("/me", func(_ context.Context, in struct{ S bites.Session[Login] }) (string, error) {
		if in.S.IsNew() {
			return "anonymous", nil
		}
		return in.S.Value.UserID, nil
	})
	srv := bitetest.Start(t, app)
	login := func(user string) string {
		sc := srv.Do(bitetest.Request{Method: "POST", Target: "/login/" + user}).Get("set-cookie")
		return strings.SplitN(sc, ";", 2)[0]
	}
	me := func(cookie string) string {
		r := srv.Do(bitetest.Request{Method: "GET", Target: "/me", Header: map[string][]string{"Cookie": {cookie}}})
		return strings.Trim(r.Text(), `"`)
	}
	laptop, phone, budi := login("ana"), login("ana"), login("budi")
	if me(laptop) != "ana" || me(phone) != "ana" || me(budi) != "budi" {
		t.Fatal("the sessions were not kept in Postgres")
	}
	if n := count(t, pool, tableName(st)); n != 3 {
		t.Fatalf("%d rows; want 3", n)
	}
	n, err := logins.RevokeOwner(ctx, "ana")
	if err != nil || n != 2 {
		t.Fatalf("RevokeOwner = %d, %v; want 2", n, err)
	}
	if me(laptop) != "anonymous" || me(phone) != "anonymous" {
		t.Fatal("a revoked session was still signed in on the next request")
	}
	if me(budi) != "budi" {
		t.Fatal("another owner was signed out")
	}
}

func TestTheStoreKeepsTheContractOfEverySessionStoreAndOwnerSessionStore(t *testing.T) {
	newOwned := func(t *testing.T) bites.OwnerSessionStore {
		st, _ := newStore(t, pgsession.Options{})
		return st
	}
	bitetest.TestSessionStore(t, func(t *testing.T) bites.SessionStore { return newOwned(t) })
	bitetest.TestOwnerSessionStore(t, newOwned)
}

// appCtx returns a ctx that bites.Now reads as clk: the ctx of a Service's Start in an app on a
// bitetest.Clock, which is what the sweep runs under.
func appCtx(t *testing.T) (context.Context, *bitetest.Clock) {
	t.Helper()
	clk := bitetest.NewClock()
	got := make(chan context.Context, 1)
	app := bites.New(bites.WithClock(clk))
	app.Service(bites.Service{Name: "clock", Start: func(c context.Context) error { got <- c; return nil }})
	bitetest.Start(t, app)
	select {
	case c := <-got:
		return c, clk
	case <-time.After(5 * time.Second):
		t.Fatal("the app did not start")
		return nil, nil
	}
}

func TestExpiryIsJudgedOnTheAppClockNotPostgresClock(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgsession.Options{})
	actx, clk := appCtx(t) // 2026-01-01: long before the database's now()
	if err := st.Save(actx, "a", []byte("x"), clk.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := st.Load(actx, "a"); err != nil || !found {
		t.Fatalf("a session live on the app's clock was not loaded: found=%v err=%v", found, err)
	}
	if n, err := st.Sweep(actx); err != nil || n != 0 {
		t.Fatalf("a sweep on the app's clock deleted a live session: %d, %v", n, err)
	}
	clk.Advance(2 * time.Hour)
	if _, found, _ := st.Load(actx, "a"); found {
		t.Fatal("a session past its expiry on the app's clock was loaded")
	}
	if n := count(t, pool, tableName(st)); n != 1 {
		t.Fatalf("%d rows before the sweep; want the expired one still there", n)
	}
	if n, err := st.Sweep(actx); err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v; want the expired row deleted on the app's clock", n, err)
	}
}

func TestTheServiceSweepsOnTheAppClock(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgsession.Options{SweepEvery: time.Minute})
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk), bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	app.Service(st.Service())
	bitetest.Start(t, app)
	// Expired for the app's clock after two minutes, live for the database's for years.
	if err := st.Save(ctx, "a", []byte("x"), clk.Now().Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		clk.Advance(time.Minute)
		time.Sleep(20 * time.Millisecond)
		if count(t, pool, tableName(st)) == 0 {
			return
		}
	}
	t.Fatal("the Service did not sweep a row that expired on the app's clock")
}

func TestATransactionOnTheCtxHoldsTheSessionChangesUntilItCommits(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgsession.Options{})
	exp := time.Now().Add(time.Hour)
	for _, id := range []string{"a1", "a2", "a3"} {
		st.SaveOwned(ctx, id, "ana", []byte(id), exp)
	}
	st.SaveOwned(ctx, "b1", "budi", []byte("b1"), exp)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	txc := pgsession.WithTx(ctx, tx)
	if n, err := st.DeleteOwner(txc, "ana", "a1"); err != nil || n != 2 {
		t.Fatalf("DeleteOwner in the transaction = %d, %v", n, err)
	}
	if err := st.SaveOwned(txc, "a4", "ana", []byte("a4"), exp); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := st.Load(txc, "a2"); found {
		t.Fatal("the transaction does not see its own delete")
	}
	if _, found, _ := st.Load(txc, "a4"); !found {
		t.Fatal("the transaction does not see its own save")
	}
	if _, found, _ := st.Load(ctx, "a2"); !found {
		t.Fatal("a delete inside an open transaction reached the pool before commit")
	}
	if _, found, _ := st.Load(ctx, "a4"); found {
		t.Fatal("a save inside an open transaction reached the pool before commit")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "a2", "a3", "b1"} {
		if _, found, _ := st.Load(ctx, id); !found {
			t.Fatalf("%s was lost by a rolled-back transaction", id)
		}
	}
	if _, found, _ := st.Load(ctx, "a4"); found {
		t.Fatal("a rolled-back save is in the table")
	}

	tx, _ = pool.Begin(ctx)
	if n, err := st.DeleteOwner(pgsession.WithTx(ctx, tx), "ana", "a1"); err != nil || n != 2 {
		t.Fatalf("DeleteOwner = %d, %v", n, err)
	}
	if err := st.Delete(pgsession.WithTx(ctx, tx), "b1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"a1": true, "a2": false, "a3": false, "b1": false} {
		if _, found, _ := st.Load(ctx, id); found != want {
			t.Fatalf("after commit, %s found = %v; want %v", id, found, want)
		}
	}
}

func TestACallOnTheCtxOfAnEndedTransactionIsTheWrappedPgxErrorAndNeverFallsBackToThePool(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgsession.Options{})
	st.SaveOwned(ctx, "a1", "ana", []byte("x"), time.Now().Add(time.Hour))
	tx, _ := pool.Begin(ctx)
	txc := pgsession.WithTx(ctx, tx)
	tx.Rollback(ctx)
	if _, _, err := st.Load(txc, "a1"); !errors.Is(err, pgx.ErrTxClosed) || !strings.HasPrefix(err.Error(), "pgsession: load: ") {
		t.Fatalf("Load = %v", err)
	}
	if err := st.Save(txc, "a2", nil, time.Now()); !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("Save = %v", err)
	}
	if _, err := st.DeleteOwner(txc, "ana", ""); !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("DeleteOwner = %v", err)
	}
	if _, found, _ := st.Load(ctx, "a1"); !found {
		t.Fatal("a session was lost")
	}
}

func TestACtxCanCarryAConnectionInsteadOfATransaction(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgsession.Options{})
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	cc := pgsession.WithTx(ctx, conn)
	if err := st.Save(cc, "a", []byte("x"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := st.Load(cc, "a"); err != nil || !found {
		t.Fatalf("Load on a connection = %v, %v", found, err)
	}
}

func TestWithTxRefusesANilQuerierAndSaysWhatToGive(t *testing.T) {
	t.Parallel()
	defer func() {
		r := fmt.Sprint(recover())
		if !strings.Contains(r, "pgsession.WithTx: q is nil: give the application's pgx.Tx") {
			t.Fatalf("panic = %q", r)
		}
	}()
	pgsession.WithTx(ctx, nil)
	t.Fatal("WithTx(nil) did not panic")
}

func TestAPasswordChangeEndsTheOtherSessionsOnlyWhenItsTransactionCommits(t *testing.T) {
	t.Parallel()
	st, pool := newStore(t, pgsession.Options{})
	app := bites.New(bites.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	logins := bites.NewSessions[Login](bites.SessionOptions{Keys: [][]byte{bytes.Repeat([]byte{7}, bites.SessionKeyLen)}, Store: st})
	app.Provide(logins.Load)
	app.Post("/login/{user}", func(ctx context.Context, in struct {
		S    bites.Session[Login]
		Path struct{ User string }
	}) (bites.NoContent, error) {
		c, err := in.S.Renew(ctx, Login{UserID: in.Path.User})
		return bites.NoContent{Cookies: []bites.Cookie{c}}, err
	})
	app.Post("/password/{outcome}", func(ctx context.Context, in struct {
		S    bites.Session[Login]
		Path struct{ Outcome string }
	}) (int, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback(ctx)
		n, err := in.S.RevokeOthers(pgsession.WithTx(ctx, tx))
		if err != nil {
			return 0, err
		}
		if in.Path.Outcome == "commit" {
			err = tx.Commit(ctx)
		}
		return n, err
	})
	app.Get("/me", func(_ context.Context, in struct{ S bites.Session[Login] }) (string, error) {
		if in.S.IsNew() {
			return "anonymous", nil
		}
		return in.S.Value.UserID, nil
	})
	srv := bitetest.Start(t, app)
	login := func() string {
		sc := srv.Do(bitetest.Request{Method: "POST", Target: "/login/ana"}).Get("set-cookie")
		return strings.SplitN(sc, ";", 2)[0]
	}
	do := func(method, target, cookie string) string {
		r := srv.Do(bitetest.Request{Method: method, Target: target, Header: map[string][]string{"Cookie": {cookie}}})
		return strings.Trim(r.Text(), `"`)
	}
	laptop, phone := login(), login()
	if got := do("POST", "/password/rollback", laptop); got != "1" {
		t.Fatalf("RevokeOthers in the transaction = %q; want 1", got)
	}
	if do("GET", "/me", phone) != "ana" {
		t.Fatal("the other session ended although the transaction rolled back")
	}
	if got := do("POST", "/password/commit", laptop); got != "1" {
		t.Fatalf("RevokeOthers in the transaction = %q; want 1", got)
	}
	if do("GET", "/me", phone) != "anonymous" {
		t.Fatal("the other session survived a committed transaction")
	}
	if do("GET", "/me", laptop) != "ana" {
		t.Fatal("the session that changed the password was ended")
	}
}
