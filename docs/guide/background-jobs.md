# Background jobs

**Keep the work a request starts but should not wait for (a mail to send, a file to read, a report to build) as jobs in Postgres with the `pgjobs` module: enqueued in the request's transaction, run by every instance, retried when they fail, and tested without a sleep.**

**Reference:** [`pgjobs`](../reference/pgjobs.md) · **Design:** [The engine](../design/engine.md)

`bites.Every` and `bites.Cron` run a function on a schedule ([Deploying](./deploying.md#periodic-work-and-a-background-worker)); they keep nothing, so a run that fails or an instance that dies loses the work. A job is the other case: a piece of work with its own arguments that must happen once the request that asked for it has committed, even when the instance that took it dies. `github.com/nevindra/bites/modules/pgjobs` keeps them in a table of the database the application already has ([ADR 0095](../adr/0095-a-job-queue-in-postgres-is-a-module-whose-jobs-are-claimed-under-a-lease-and-delivered-at-least-once.md)). It is a module of its own, so an application that does not use it neither downloads nor links it:

```sh
go get github.com/nevindra/bites/modules/pgjobs
```

## A kind, an enqueue and the Service

**Register each kind of job with the type of its arguments and the function that runs one, enqueue it through the transaction of the write it belongs to, and give the Client's Service to the app.** The arguments are JSON, so a job carries the ids of what it works on, not the data.

```go
type Welcome struct {
	UserID int64 `json:"user_id"`
}

type SignUp struct {
	Body struct{ Email string }
}

func newApp(ctx context.Context, pool *pgxpool.Pool, broker *pgnotify.Broker, mail Mailer) (*bites.App, error) {
	jobs := pgjobs.New(pgjobs.Options{Pool: pool, Notify: broker})
	if err := jobs.CreateTable(ctx); err != nil { // or paste jobs.Schema() into a migration
		return nil, err
	}
	welcome := pgjobs.Register(jobs, "welcome-mail", func(ctx context.Context, job pgjobs.Job[Welcome]) error {
		return mail.SendWelcome(ctx, job.Args.UserID, job.ID.String()) // the id makes a second send a no-op
	}, pgjobs.KindOptions{MaxAttempts: 10, Timeout: 30 * time.Second})

	app := bites.New()
	app.Service(jobs.Service())
	app.Post("/signup", func(ctx context.Context, in SignUp) (bites.Created[User], error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return bites.Created[User]{}, err
		}
		defer tx.Rollback(ctx)
		user, err := insertUser(ctx, tx, in.Body.Email)
		if err != nil {
			return bites.Created[User]{}, err
		}
		if _, err := welcome.Enqueue(ctx, tx, Welcome{UserID: user.ID}); err != nil {
			return bites.Created[User]{}, err
		}
		return bites.Created[User]{Body: user}, tx.Commit(ctx)
	})
	return app, nil
}
```

- **The job exists if and only if the user does.** A rollback, a failed insert or a crash before the commit leaves no job; a commit leaves both. Through the pool instead of `tx`, the job is stored at once on its own.
- **The job starts as soon as the transaction commits.** With `Options.Notify` (the [`pgnotify`](../reference/pgnotify.md) Broker the application runs for its Topics) the enqueue sends a wake-up in the same transaction, which every instance hears at the commit; without one, the workers find it at their next poll, one second by default.
- **Every instance that registers the Service runs jobs.** Each claims due jobs with `FOR UPDATE SKIP LOCKED`, so two never take the same one, and holds each under a lease its heartbeat extends. An instance that only enqueues (a migration tool, a CLI) registers the kinds and does not register the Service.
- **Register every kind before the app starts.** A Register after the Service started, a kind registered twice, or a queue not in `Options.Queues` is a panic naming the line, as a route's mistakes are.

## When a job fails

**A handler that returns an error is tried again after a back-off: one second after the first failure, doubling up to an hour, with a fifth of jitter, until its attempts are spent (20 by default). It is then kept as failed with its last error, for `Client.Retry` or a person to look at.**

```go
func sendWelcome(ctx context.Context, job pgjobs.Job[Welcome]) error {
	user, err := users.Get(ctx, job.Args.UserID)
	if errors.Is(err, ErrNoUser) {
		return pgjobs.Fail(err) // it can never succeed: fail it now
	}
	if err != nil {
		return err // tried again after the back-off
	}
	if user.MailboxFull {
		return pgjobs.Snooze(time.Hour) // not a failure: run again in an hour, the attempt not counted
	}
	return mailer.Send(ctx, user.Email, welcomeText, job.ID.String())
}
```

- **A job runs at least once, not exactly once.** An instance that dies after the mail left and before the completion was written leaves the job to run again once its lease runs out. Key what the handler writes on `job.ID` (or `job.ScheduledAt` for a periodic job) so a second run changes nothing.
- **A panic is a failed attempt**, logged with its stack. An attempt that runs past its kind's `Timeout` has its `ctx` cancelled and is a failed attempt too.
- **A kind may give its own back-off**: `KindOptions{Backoff: func(attempt int) time.Duration { return time.Minute }}`.

## Later, once, and on a schedule

**`EnqueueOptions` sets when a job runs and whether it may be enqueued twice.**

```go
// in an hour, unless a reminder for this invoice is already waiting or running
_, err = reminder.Enqueue(ctx, tx, Reminder{InvoiceID: id}, pgjobs.EnqueueOptions{
	Delay:     time.Hour,
	UniqueKey: "invoice:" + strconv.FormatInt(id, 10),
})
```

A unique key is held by a job of the same kind while it is available (due, or waiting for its time or a back-off) or running; the second enqueue then stores nothing and returns the first job's id with `Duplicate` set. Once the job has completed, failed or been cancelled, the key is free again. `Priority` (higher first, -100 to 100), `Queue` and `MaxAttempts` override the kind's.

**A periodic job is a kind with a schedule, run once per period by one of the instances, with no leader.**

```go
sweep := pgjobs.Register(jobs, "sweep-sessions", sweepSessions)
sweep.Every("sweep-sessions", 15*time.Minute, Sweep{})
digest := pgjobs.Register(jobs, "daily-digest", sendDigest)
digest.Cron("daily-digest", "0 9 * * mon-fri", jakarta, Digest{})
```

The instance that first records a period enqueues its job, in one transaction, so a period is one job however many instances look; the job then runs, retries and fails as any other. A period that every instance missed while they were all stopped runs once when the first starts.

## Queues and how many run at once

**`Options.Queues` names the queues and how many of each one instance runs at once**: `{"default": 10}` when it is empty. Give a slow kind its own queue so it cannot take every worker:

```go
jobs := pgjobs.New(pgjobs.Options{Pool: pool, Notify: broker, Queues: map[string]int{"default": 10, "extract": 2}})
extract := pgjobs.Register(jobs, "extract-text", extractText, pgjobs.KindOptions{Queue: "extract", Timeout: 5 * time.Minute})
```

## Shutting down

**`Shutdown` stops the claiming while the app still serves, waits for the running jobs until two seconds before its deadline, then cancels the rest and puts them back**, with their attempt given back, so another instance takes them at once. A handler that ignores its `ctx` keeps its job until the lease runs out (30 seconds by default) and is logged by id. Register the Service after the pool's, so the pool is still open when the jobs end.

## Looking at the queue

`Counts`, `List` and `Get` read the jobs for an operator page; `Retry` runs a failed job again and `Cancel` stops one. An [`Observer`](../reference/pgjobs.md#pgjobsobserver) sees each attempt, with the trace of the request that enqueued it in its `ctx`, the way the `otel` module sees a request.

## Testing

**Give the Client a `bitetest.Clock` and call `Work`: it runs every due job on the test's goroutine and returns how many, so a test sends a request, runs the jobs and looks at the result, with no Service and no sleep.** `Advance` reaches a delay, a back-off and the next period. Build the test's app without `jobs.Service()`, so that only `Work` runs the jobs.

```go
func TestSignUpSendsTheWelcomeThenTheReminderADayLater(t *testing.T) {
	clk := bitetest.NewClock()
	jobs := pgjobs.New(pgjobs.Options{Pool: testPool(t), Clock: clk})
	mail := &fakeMailer{}
	app := newAppWith(jobs, mail, bites.WithClock(clk))
	srv := bitetest.Start(t, app)

	srv.Do(bitetest.Request{Method: "POST", Target: "/signup", Body: []byte(`{"email":"ada@example.com"}`), Header: map[string][]string{"Content-Type": {"application/json"}}})
	if n, err := jobs.Work(t.Context()); err != nil || n != 1 || len(mail.sent) != 1 {
		t.Fatalf("Work ran %d (%v); sent %v", n, err, mail.sent)
	}
	clk.Advance(24 * time.Hour)
	if n, _ := jobs.Work(t.Context()); n != 1 {
		t.Fatalf("a day later Work ran %d, want the reminder", n)
	}
}
```

The module's own tests need a database and read it from `BITES_PG_URL`; a test of the application does the same with its own variable, and gives each test its own table (`Options.Table`) to run in parallel.
