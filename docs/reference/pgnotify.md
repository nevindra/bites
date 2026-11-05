# pgnotify

**The module that carries a Topic between the instances of an application over Postgres `LISTEN` and `NOTIFY`, with one listening connection that reconnects, a payload limit that is an error and a gap signal.**

**Guide:** [WebSockets across several instances](../guide/websockets.md#across-several-instances) · **Design:** [WebSockets](../design/websocket.md)

`github.com/nevindra/bites/modules/pgnotify` is a module of its own ([ADR 0070](../adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)), so the core carries none of it. It depends on `github.com/jackc/pgx/v5` at v5.10.0. Its names are not checked by the documentation tests, which read the root, `bitetest` and `stdhttp`; its own tests are run with `cd modules/pgnotify && go test ./...`, and the tests that need a database run when `BITES_PG_URL` names one and are skipped, with a message naming it, otherwise. The decision is [ADR 0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md).

One connection of each instance does `LISTEN` on one Postgres channel and carries every Topic; the notification is the topic name, a newline and the payload. Publishing uses a pool and `SELECT pg_notify($1, $2)`.

```go
broker := pgnotify.New(pgnotify.Options{URL: dbURL, Pool: pool})
go broker.Run(ctx)
defer broker.Close()
room := bites.NewTopic[Frame]("room.7", broker, bites.TopicOptions{})
```

### `pgnotify.New`

```go
func New(o Options) *Broker
```

New returns a Broker and connects nothing until `Run`. It panics, `pgnotify.New: <reason>` and `registered at file.go:N` on the next line, when `URL` and `Config` are both empty or both given, `URL` does not parse, `Channel` is over 63 bytes or holds a NUL, a duration is negative, or `MaxBackoff` is below `MinBackoff`.

### `pgnotify.Options`

```go
type Options struct {
	URL            string
	Config         *pgx.ConnConfig
	Channel        string
	Pool           *pgxpool.Pool
	Log            *slog.Logger
	OnState        func(listening bool)
	MinBackoff     time.Duration
	MaxBackoff     time.Duration
	PublishTimeout time.Duration
	KeepAlive      time.Duration
}
```

`URL` is the Postgres connection string of the `LISTEN` connection, and of the publishing pool when `Pool` is nil. `Config` is the same as a `*pgx.ConnConfig`, for a program that already holds one: `Config: pool.Config().ConnConfig` listens on the database the pool reaches, including a field set after parsing (`ConnConfig.Database` on a copied template database), which `ConnString()` would not name. One of the two is required, and giving both is refused, since they could name two databases. The line `pgnotify: listening` names the channel, the database and the host, so a broker listening on the wrong database shows in the log. `Channel` is the Postgres channel every Topic is carried on, `bites` when empty: instances that share Topics use the same one, applications that share a database use different ones. `Pool` is the pool Publish uses; nil makes the Broker open its own (no connection until the first Publish) and `Close` closes it, while a pool given is the caller's and stays open. `Log` is `slog.Default()` when nil. `OnState` is called from the goroutine of `Run` when the connection becomes live (true) and when it is lost (false): it is how an application fails its readiness, `app.SetReady`, while it cannot hear; it must not block. The reconnect pause starts at `MinBackoff` (100 ms), doubles on each failed attempt up to `MaxBackoff` (10 s), has 20% jitter and resets once a connection was listening. `PublishTimeout` (5 s) bounds one Publish. `KeepAlive` (30 s) is how often an idle connection is checked with `SELECT 1`, so one dropped without a reset is found.

### `pgnotify.Broker`

```go
type Broker struct { /* unexported */ }
```

Broker implements [`bites.Broker`](./websocket.md#broker). Make one with `New`; the zero value is not usable. It is safe for concurrent use.

### `pgnotify.Broker.Run`

```go
func (b *Broker) Run(ctx context.Context) error
```

Run keeps the `LISTEN` connection until `ctx` is done or `Close` is called, and then returns nil. A connection that cannot be made or that drops is retried after the pause above; each failure is logged at Warn with `err` and `retry_in`, each success at Info. When the connection is back and listening again, the subscriptions that were live when it dropped, and those made while it was down, are told through their `gap` (a Topic's `OnGap`). It returns an error only when called twice. Run it on its own goroutine; under a lifecycle Service it is `Start: go broker.Run(ctx)` and `Stop: broker.Close` ([ADR 0081](../adr/0081-a-topic-reaches-other-instances-through-a-broker-and-postgres-is-a-module.md)).

### `pgnotify.Broker.Close`

```go
func (b *Broker) Close() error
```

Close stops `Run`, waits for it to return, and closes the pool if the Broker opened it. Publish returns `ErrClosed` afterwards. Close the Broker before the pool it was given. Safe to call more than once; it returns nil.

### `pgnotify.Broker.Listening`

```go
func (b *Broker) Listening() bool
```

Listening reports whether the `LISTEN` connection is live.

### `pgnotify.Broker.Publish`

```go
func (b *Broker) Publish(ctx context.Context, name string, payload []byte) error
```

Publish sends `payload` to every instance listening on the channel, this one included. The notification is the name, a newline and the payload and must be at most `MaxNotification` (7999) bytes: a larger one is the error `pgnotify: the notification for "room.1" is 8000 bytes (name, newline and payload), over the Postgres limit of 7999: ...`, checked before the pool is used, and nothing is sent. A payload that is not valid UTF-8 or holds a NUL is refused too. Notifications of one publisher that waits for each Publish arrive in order, and two publishers are ordered by Postgres, the same on every instance. A Topic calls this; an application calls `Topic.Publish`.

### `pgnotify.Broker.Subscribe`

```go
func (b *Broker) Subscribe(name string, deliver func([]byte), gap func()) (stop func())
```

Subscribe registers `deliver` for `name`. Nothing is sent to Postgres: the one connection already carries every name. Deliveries run on the goroutine that reads the connection, one at a time, in the order Postgres sent them. A Topic calls this; an application does not.

### `pgnotify.Broker.Accepts`

```go
func (b *Broker) Accepts(name string) error
```

Accepts reports whether `name` can be carried: not empty, at most 255 bytes, valid UTF-8, with no newline (the separator) and no NUL. `bites.NewTopic` calls it, so such a name is a Registration panic.

### `pgnotify.MaxNotification`

```go
const MaxNotification = 7999
```

MaxNotification is the largest notification Postgres accepts, in bytes: it refuses 8000 and over. A test against a real Postgres holds the number.

### `pgnotify.ErrClosed`

```go
var ErrClosed = errors.New("pgnotify: the Broker is closed")
```

ErrClosed is what `Publish` returns after `Close`.
