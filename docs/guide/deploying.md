# Deploying

**To run an app you listen on one port or several, serve TLS with certificates you can reload, catch the signal yourself, call `Shutdown`, read the client's address from your balancer's headers or PROXY protocol, and decide whether a load balancer or the app compresses.**

**Reference:** [`App.Listen`, `App.Shutdown`, options](../reference/app.md), [health and readiness](../reference/observability.md#health-readiness-and-the-shutdown-delay) · **Design:** [The engine](../design/engine.md), [observability](../design/observability.md)

bites has its own HTTP/1.1 engine and one app holds no global state ([ADR 0005](../adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md), [ADR 0026](../adr/0026-no-global-state-and-one-behaviour.md)), so deploying is a few lines of your own `main`. This page writes them. Settings that differ between environments (an address, a pool size, a key) are read by the [configuration module](./configuration.md).

## Listening on a port

**[`app.Listen(addr)`](../reference/app.md#applisten) serves on a TCP address until `Shutdown` or `Close`, and returns [`ErrServerClosed`](../reference/app.md#errserverclosed) then.** It returns any other error from listening at once, such as an address already in use.

<!-- compiles: decls -->
```go
func main() {
	app := bites.New()
	if err := app.Listen(":8080"); err != nil && !errors.Is(err, bites.ErrServerClosed) {
		log.Fatal(err)
	}
}
```

[`app.Serve(ln)`](../reference/app.md#appserve) does the same on any `net.Listener`, which is how you serve on a listener you made yourself (a systemd socket, say) and how [a test serves over memory](./testing.md). More than one `Serve` may run on one app. The first call of any of them freezes the routes: adding a route, middleware or Provider afterwards panics ([ADR 0048](../adr/0048-groups-and-the-order-things-run-in.md)).

## Several listeners and a Unix socket

**One app serves on as many listeners as you start, each on its own goroutine, and `Shutdown` and `Close` reach every one of them.** Plain and TLS, TCP and Unix may be mixed: they share the routes and `MaxConns`, and each logs one line as it starts, `bites: listening network=unix addr=/run/app/app.sock tls=false proxy_protocol=false` ([ADR 0068](../adr/0068-one-app-serves-several-listeners-and-a-unix-socket-is-a-socket-like-tcp.md)).

<!-- compiles: decls -->
```go
func serveAll(app *bites.App, cfg *tls.Config) error {
	errs := make(chan error, 3)
	go func() { errs <- app.Listen(":8080") }()                        // the orchestrator's probes
	go func() { errs <- app.ListenTLS(":8443", cfg) }()               // clients
	go func() { errs <- app.ListenUnix("/run/app/app.sock", 0o660) }() // nginx on the same host
	return <-errs // the first to return: an error, or ErrServerClosed after Shutdown
}
```

**[`app.ListenUnix(path, perm)`](../reference/app.md#applistenunix) makes the socket file with the permission bits you give, and removes it when it closes.** Connecting needs write permission, so `0o660` lets the process's group connect: put the proxy's user in that group. A socket left behind by a process that crashed is removed first; a socket another process answers on, or a file that is not a socket, is an error and is left alone. A Unix connection parks when idle and is watched for a client that leaves exactly as a TCP one is, and it has no peer address: `Request.RemoteAddr()` is the zero `netip.AddrPort`, so a log of client addresses behind a Unix socket needs the proxy's headers, believed when `TrustedProxies.UnixSockets` is set, or [the PROXY protocol](#the-proxy-protocol).

## Serving HTTPS

**[`app.ListenTLS(addr, cfg)`](../reference/app.md#applistentls) and [`app.ServeTLS(ln, cfg)`](../reference/app.md#appservetls) take a `*tls.Config`, and loading the certificate is your job.** The engine wraps each accepted connection with `crypto/tls` and runs the handshake on the connection's own goroutine, within `Limits.HandshakeTimeout` (10 seconds) before the first request ([ADR 0017](../adr/0017-tls-now-http2-later.md)).

<!-- compiles: decls -->
```go
func serveTLS(app *bites.App) error {
	cert, err := tls.LoadX509KeyPair("cert.pem", "key.pem")
	if err != nil {
		return err
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	return app.ListenTLS(":8443", cfg)
}
```

Only HTTP/1.1 is negotiated until the HTTP/2 engine exists. A config is refused with a panic, before anything is served, when it is nil, has no certificate source (`Certificates`, `GetCertificate` or `GetConfigForClient`), or offers an application protocol other than `http/1.1` (and `acme-tls/1`, below) in `NextProtos`. `h2` above all: a client that agreed to it would be sent HTTP/1.1 it cannot read. Leave `NextProtos` empty unless you use an ACME client.

```text
ListenTLS: the TLS config has no certificate: set Certificates, GetCertificate or GetConfigForClient
	registered at main.go:30
```

## Renewing the certificate without a restart

**Give the config a [`Certs`](../reference/app.md#certs)'s `GetCertificate` and call `Reload` when the files change.** Handshakes during a reload finish with the old certificate and the next ones get the new one; open connections, WebSockets included, are not touched. A reload that fails (a file half written, a key that is not the certificate's, a certificate already expired) returns its error and the old certificate goes on serving ([ADR 0067](../adr/0067-a-certificate-is-reloaded-through-get-certificate-and-a-failed-reload-changes-nothing.md)).

<!-- compiles: decls -->
```go
func serveReloadable(app *bites.App) error {
	certs, err := bites.LoadCerts("/etc/tls/tls.crt", "/etc/tls/tls.key")
	if err != nil {
		return err
	}
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := certs.Reload(); err != nil {
				slog.Error("certificate not reloaded; the old one is still served", "err", err)
			}
		}
	}()
	return app.ListenTLS(":8443", &tls.Config{GetCertificate: certs.GetCertificate})
}
```

Nothing watches the files for you: SIGHUP, a ticker, or the hook of the tool that renews them triggers the reload. Leave `Certificates` empty in that config.

## Certificates from an ACME CA

**An ACME client's `GetCertificate`, autocert's for one, works with `acme-tls/1` in `NextProtos`.** The CA's tls-alpn-01 validation is answered by the handshake itself and its connection is closed at once; every other client still gets HTTP/1.1 ([ADR 0017](../adr/0017-tls-now-http2-later.md)). Listing `acme-tls/1` with only static `Certificates` panics, since nothing could answer the challenge.

```go
m := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist("api.example.com"), Cache: autocert.DirCache("certs")}
app.ListenTLS(":443", &tls.Config{GetCertificate: m.GetCertificate, NextProtos: []string{"http/1.1", acme.ALPNProto}})
```

## Client certificates (mTLS)

**Ask for client certificates with the config's `ClientAuth` and `ClientCAs`, and read the verified client in a Provider with [`Request.VerifiedChains`](../reference/middleware.md#requestverifiedchains).** `crypto/tls` does the verifying; the request offers only chains it verified, never a certificate that was asked for and not checked ([ADR 0066](../adr/0066-client-certificates-are-verified-by-the-tls-config-and-read-as-verified-chains.md)).

<!-- compiles: decls -->
```go
type Caller struct{ Name string }

func caller(_ context.Context, req bites.Request, _ struct{}) (Caller, error) {
	chains := req.VerifiedChains()
	if len(chains) == 0 {
		return Caller{}, bites.Unauthorized("a client certificate is required")
	}
	return Caller{Name: chains[0][0].Subject.CommonName}, nil
}

func serveMTLS(app *bites.App, server tls.Certificate, clientCAs *x509.CertPool) error {
	app.Provide(caller)
	return app.ListenTLS(":8443", &tls.Config{
		Certificates: []tls.Certificate{server},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.VerifyClientCertIfGiven, // or RequireAndVerifyClientCert for every route
	})
}
```

A config that verifies client certificates with no `ClientCAs` panics: `crypto/tls` would then accept a certificate from any public CA. Revocation is not checked; a `VerifyPeerCertificate` of your own can.

## Shutting down without dropping requests

**The framework installs no signal handler: catching the signal and calling [`Shutdown`](../reference/app.md#appshutdown) is the application's own Go.** A library that takes over signals surprises the program that embeds it ([ADR 0029](../adr/0029-shutdown-drains-and-then-closes.md)).

<!-- compiles: decls -->
```go
func run(app *bites.App, addr string) error {
	stop, cancelSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelSignals()

	served := make(chan error, 1)
	go func() { served <- app.Listen(addr) }()

	select {
	case err := <-served:
		return err // it never started
	case <-stop.Done():
	}

	grace, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := app.Shutdown(grace); err != nil {
		return err // the grace ran out, and what was left has been closed
	}
	if err := <-served; !errors.Is(err, bites.ErrServerClosed) {
		return err
	}
	return nil
}
```

`Shutdown` stops accepting, closes idle connections at once, and lets requests in flight finish, marking their connections `Connection: close`. Then:

- A [live stream](./streaming.md#ending-streams-when-the-client-leaves-or-the-server-stops) (an `SSE`, or a `Stream` with `Live` set) has its context cancelled with the cause `ErrServerClosed`, so a deploy does not wait the full grace for it. A plain `Stream`, a large download, drains.
- Every [WebSocket](./websockets.md#closing-and-the-1001-at-shutdown) is sent a close with status 1001, each after a short random delay so clients do not reconnect in the same millisecond. [How wide that delay is](#spreading-the-websocket-closes-of-a-rolling-deploy) is yours to set.
- When the context ends first, whatever is still open is closed, its context cancelled, and `Shutdown` returns the context's error.

`Listen` returns `ErrServerClosed` as soon as `Shutdown` starts when the app has no [services](#services-that-live-as-long-as-the-app), so `main` must wait for `Shutdown` to return: if it exits on that first return, the drain is cut. With services `Listen` returns after their `Stop` functions. Pick a grace shorter than your orchestrator's kill timeout (Kubernetes' default is 30 seconds). **[`app.Close()`](../reference/app.md#appclose) stops accepting and closes every connection at once**, for a test or a second signal. Both are safe before the app started serving.

## Spreading the WebSocket closes of a rolling deploy

**During a rolling deploy every WebSocket of the old instance reconnects to the instances that remain; [`WithGoAwaySpread`](../reference/websocket.md#withgoawayspread) sets the longest window over which `Shutdown` spreads those closes.** The window is 1 ms a socket, at most the spread (3 seconds when you give nothing), and at most half of what is left of `Shutdown`'s context. With 20,000 sockets the default tells about 6,700 clients a second to leave; if the instances that stay cannot take that, give a wider window:

<!-- compiles: decls -->
```go
func newChatApp() *bites.App {
	return bites.New(bites.WithGoAwaySpread(10 * time.Second))
}
```

- **It is a ceiling, not a delay.** 2,000 sockets under 10 seconds are still spread over 2 seconds, so a small deploy is not slowed.
- **Give `Shutdown` a context longer than twice the spread**, counted from when the drain starts: the `BeforeShutdown` of your services and [`WithShutdownDelay`](../reference/observability.md#withshutdowndelay) spend the context first. Set the orchestrator's grace period to match (`terminationGracePeriodSeconds` in Kubernetes).
- **Read the log.** `Shutdown` logs `bites: shutdown: WebSocket Close 1001 spread` with the `sockets`, the `window` it used and the `asked` window. When half of the context cut the window it is a warning that says so; the cure is a longer grace period, not a larger spread.
- **Clients still decide when they arrive.** Write them to reconnect with a back-off and jitter of their own; a client that retries at once undoes the spread.
- `WithGoAwaySpread` is not `WithShutdownDelay`: the delay keeps the listener open for a balancer that learns late, the spread paces the clients leaving. `New` panics for a zero or negative window and for the option given twice.

## Services that live as long as the app

**A database pool, a queue consumer, a scheduler or a registry entry lives exactly as long as the app does: give it to [`app.Service`](../reference/app.md#appservice) as a [`Service`](../reference/app.md#service), and the app starts it before the first accept and stops it after the drain.** You write no ordering around `Listen` ([ADR 0079](../adr/0079-services-start-after-the-app-binds-and-stop-in-reverse-around-shutdown.md)).

<!-- compiles: decls -->
```go
func newOrdersApp(pool *sql.DB, queue *Consumer) *bites.App {
	app := bites.New()
	app.Service(bites.Service{
		Name:  "orders-db",
		Start: func(ctx context.Context) error { return pool.PingContext(ctx) },
		Stop:  func(context.Context) error { return pool.Close() },
	})
	app.Service(bites.Service{
		Name:           "orders-queue",
		Start:          func(ctx context.Context) error { go queue.Run(ctx); return nil }, // ctx lasts until Stop
		BeforeShutdown: func(ctx context.Context) error { return queue.StopTaking(ctx) },
		Stop:           func(ctx context.Context) error { return queue.Wait(ctx) },
	})
	return app
}

type Consumer struct{}

func (*Consumer) Run(context.Context)                 {}
func (*Consumer) StopTaking(context.Context) error { return nil }
func (*Consumer) Wait(context.Context) error       { return nil }
```

- **`Start` runs once the address is bound and before the first connection is accepted, in the order you registered the services.** An address in use is returned before any service starts. A connection that arrives meanwhile waits in the kernel's backlog and is answered after the last `Start`, so give a slow starter a start-up probe in your platform.
- **`Start` returns when the service is running; it does not block.** Its `ctx` is the service's life, cancelled just before its `Stop`, so `go run(ctx)` is the right body for a worker.
- **A `Start` that fails stops what started, in reverse, and `Listen` returns `bites: service "orders-queue": Start: <cause>`.** `errors.Is` and `errors.As` see the cause. The app never serves without a service it was written to have.
- **`BeforeShutdown` runs when `Shutdown` starts, after readiness fails and before the shutdown delay**, while the app still serves: leave a registry, stop taking work. `Close` skips it.
- **`Stop` runs after the drain, in the reverse of the order you registered**, so register the pool before the worker that uses it. Requests in flight can still use every service.
- **`Listen` returns `ErrServerClosed` only after the `Stop` functions have returned**, so `main` returning after `Listen` does not cut them. A function that ignores its context is abandoned and logged when `Shutdown`'s context ends; [`WithServiceTimeout`](../reference/app.md#withservicetimeout) (30 seconds by default) bounds `Start` and any function called with a context already done. Give `Shutdown` a context with a deadline, as above: with none, a hung `BeforeShutdown` is waited for.

A `Start` or `BeforeShutdown` must not call `Shutdown` or `Close` on its own app and wait for it; call it from its own goroutine.

### Periodic work and a background worker

**[`bites.Every`](../reference/app.md#every) calls a function once per interval for the life of the app, and [`bites.Worker`](../reference/app.md#worker) runs a function on its own goroutine until `Stop`; both return a `Service` for `app.Service`** ([ADR 0079](../adr/0079-services-start-after-the-app-binds-and-stop-in-reverse-around-shutdown.md)). Use `Every` for a sweep of expired rows, a purge of rows deleted more than 30 days ago, or a check each minute for meetings that start. Use `Worker` for a loop of your own, such as a consumer of a channel. The first call of `Every` is one interval after the start; a job that must also run right after the start (warm a cache, reconcile what a crash left behind) gives `bites.EveryOptions{AtStart: true}` as its last argument, and that call runs on the Service's goroutine, so the start does not wait for it and `Stop` cancels it like any other call.

<!-- compiles: decls -->
```go
func registerBackground(app *bites.App, db *Rows, outbox chan string) {
	app.Service(bites.Every("purge-deleted", time.Hour, func(ctx context.Context) error {
		return db.PurgeDeletedBefore(ctx, bites.Now(ctx).AddDate(0, 0, -30))
	}))
	app.Service(bites.Every("meeting-check", time.Minute, func(ctx context.Context) error {
		return db.NotifyStarting(ctx, bites.Now(ctx))
	}))
	app.Service(bites.Worker("mailer", func(ctx context.Context) error {
		for {
			select {
			case <-ctx.Done():
				return nil // the app is stopping
			case to := <-outbox:
				bites.Logger(ctx).Info("sending", "to", to)
			}
		}
	}))
}

type Rows struct{}

func (*Rows) PurgeDeletedBefore(context.Context, time.Time) error { return nil }
func (*Rows) NotifyStarting(context.Context, time.Time) error     { return nil }
```

- **The first call of `Every` is one interval after the app started serving, not at start**, so a start never waits for the work and a deploy of many instances does not run every sweep at once. Give `bites.EveryOptions{AtStart: true}` when you want a run right after the start.
- **Calls are serial.** A run longer than the interval delays the next and never overlaps it; the ticks it missed are dropped, not run in a burst, and the log says at Warn how many (`bites: job took longer than its interval`). With several instances each runs the job: make it safe to run twice (a `DELETE` of old rows is; a mail is not, so guard it with a row lock or a claim in the database).
- **An error from the function is logged at Warn and the loop goes on; a panic is recovered, logged at Error and the loop goes on.** A `Worker` that returns before `Stop`, with an error or nil, or panics, is logged at Error with its name and is not restarted: loop inside it when it must survive.
- **`ctx` is the service's life, and `bites.Now(ctx)` and `bites.Logger(ctx)` read the app's clock and logger.** `Stop` cancels it and waits for a call in progress, so pass `ctx` to the database call; one that ignores it is abandoned and logged when `Shutdown`'s context ends.
- **There is no cron expression and no jitter option.** For a schedule at a time of day, use `Every` with a short interval and compare `bites.Now(ctx)` to the time you want.

## Health, readiness and the shutdown delay

**[`WithHealth`](../reference/observability.md#withhealth) answers a liveness and a readiness path for your orchestrator, and [`WithShutdownDelay`](../reference/observability.md#withshutdowndelay) keeps serving for a moment after `Shutdown` starts, so a balancer stops sending before the listener closes** ([ADR 0052](../adr/0052-health-and-readiness-follow-shutdown.md)).

<!-- compiles: decls -->
```go
func newProbed() *bites.App {
	return bites.New(
		bites.WithHealth(bites.Health{Live: "/livez", Ready: "/readyz"}),
		bites.WithShutdownDelay(5*time.Second),
	)
}
```

- `/livez` is 200 `ok` for as long as the app serves, Shutdown included, so the orchestrator does not kill a process that is draining.
- `/readyz` is 200 `ok` while the app wants traffic, and a 503 problem+json from the moment `Shutdown` is called, or while [`app.SetReady(false)`](../reference/observability.md#appsetready) holds (a cache still warming, a maintenance window).
- Both are answered before app middleware, so an auth middleware does not refuse a probe and a rate limiter does not count one; they are not in the API document, not in the access log and not in the metrics.
- Readiness checks no dependency. A readiness that pinged Postgres would take every replica out of the balancer at once when Postgres blips, turning a database outage into an outage of the whole API. Call `SetReady` from your own check if you want that anyway.

```http
GET /readyz HTTP/1.1
Host: api.example.com

HTTP/1.1 503 Service Unavailable
Content-Type: application/problem+json

{"title":"Service Unavailable","status":503,"detail":"the app is shutting down"}
```

**Set the delay behind any balancer.** Kubernetes removes a terminating pod's endpoint, and kube-proxy and the ingress apply that some seconds later; an AWS target group notices a failed readiness only after its interval times its threshold. During the delay the listener is open and requests are served as usual, with readiness failing; then `Shutdown` drains as above. The delay is bounded by half of what is left of `Shutdown`'s context, so with a 25-second grace and a 5-second delay the drain still has 20 seconds; a second `Shutdown` waits for the same delay, and `Close` ends it. It runs on the app's clock, so a test moves a `bitetest.Clock` instead of waiting. A registered route at a health path is refused at Registration, naming both.

## Compressing responses

**Compression is off by default; leave it off when a load balancer or CDN in front of the app compresses.** [`WithCompression`](../reference/app.md#withcompression) turns it on with the encoders you list, in your order of preference, and the engine compresses a body of at least `MinSize` bytes with the one the request's `Accept-Encoding` weighs highest ([ADR 0031](../adr/0031-gzip-only-and-off-by-default.md), [ADR 0061](../adr/0061-compression-chooses-among-the-apps-encoders-by-the-clients-weights-then-the-apps-order.md)). gzip is in the core; zstd and brotli are modules, so the core keeps no dependency and you add only what you use:

```sh
go get github.com/nevindra/bites/modules/brotli github.com/nevindra/bites/modules/zstd
```

```go
import (
	bites "github.com/nevindra/bites"
	"github.com/nevindra/bites/modules/brotli"
	"github.com/nevindra/bites/modules/zstd"
)

func newCompressed() *bites.App {
	return bites.New(bites.WithCompression(bites.Compression{
		Encoders: []bites.Encoder{brotli.Encoder(), zstd.Encoder(), bites.GzipEncoder()},
	}))
}
```

**The order matters for every browser.** A browser sends `Accept-Encoding: gzip, deflate, br, zstd`, every coding with the same weight, so the first of your list it accepts wins; a client that weighs them (`gzip;q=1, br;q=0.5`) gets its heaviest. Brotli first, then zstd, then gzip is the order the measurements give: on JSON, brotli at the module's level made answers 14 to 42% smaller than gzip and zstd 8 to 29% smaller, each at about gzip's CPU. curl and most client libraries ask for gzip only, or nothing, and get that.

**With gzip alone**, [`WithGzip(Gzip{MinSize})`](../reference/app.md#withgzip) is the short form:

<!-- compiles: decls -->
```go
func newGzipped() *bites.App {
	return bites.New(bites.WithGzip(bites.Gzip{MinSize: 2 * bites.MinCompressSize}))
}
```

`MinSize` is 1 KiB when zero ([`MinCompressSize`](../reference/app.md#mincompresssize)) and cannot be lower: under about 1.2 KiB an answer fits one TCP segment, so compressing saves bytes and no packets. A lower value, either option given twice, or both given, panics, and so does an encoder listed twice. The levels are fixed: gzip at `BestSpeed`, zstd at its fastest with a 1 MiB window, brotli at level 1. It is done by the engine on the finished body, so a middleware sees the uncompressed answer, a [stream](./streaming.md#what-a-stream-cannot-do) and a file are never compressed, every answer carries `Vary: Accept-Encoding`, and a strong `ETag` is weakened on a compressed one.

Turn it on when the app answers browsers directly with JSON lists above a kilobyte or two. Leave it off behind a compressing balancer, where it would compress twice, and when the CPU is the scarce resource: it costs about 3 to 45 microseconds on every answer of 1 to 64 KiB (measured in [ADR 0061](../adr/0061-compression-chooses-among-the-apps-encoders-by-the-clients-weights-then-the-apps-order.md)), paid per compressed response, not hidden in the budget of [ADR 0003](../adr/0003-the-trade-budget.md). An answer that reflects a secret (a CSRF token, a session id) next to text an attacker chose can leak the secret through the compressed length (BREACH), whatever the coding: do not compress such pages, or set `Cache-Control: no-transform` on them, which the engine respects.

## Idle connections and memory

**A connection idle for 100 ms is parked on the engine's epoll and its goroutine ends, which takes about 2 KB off every idle connection.** Bytes arriving give it a goroutine again. Parking is used only where it can be (Linux, TCP and Unix sockets), and the cost is a goroutine start and an epoll registration when a parked connection is woken ([ADR 0005](../adr/0005-our-own-http1-engine-with-a-goroutine-per-connection.md), which has the measurements).

[`WithoutParking()`](../reference/app.md#withoutparking) keeps every idle connection on a goroutine of its own, as before parking. Use it to compare against parking, or to go back should it misbehave; the start-up log says `idle connections park` is `off` then.

## Logging

**[`WithLogger(l)`](../reference/app.md#withlogger) sets the `slog.Logger` for the start-up settings, handler errors and recovered panics; the default is `slog.Default()`.**

<!-- compiles: decls -->
```go
func newLogged() *bites.App {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return bites.New(bites.WithLogger(logger))
}
```

An error that a handler returns without a status is a 500 with no detail to the client, and the real reason is logged with the method and path. A panic is recovered, logged with its stack and answered 500 ([ADR 0028](../adr/0028-panics-are-recovered-by-the-engine.md)). One log line per request is the [`AccessLog`](./first-party-middleware.md#logging-each-request) Observer's; metrics and traces are on [the observability page](./observability.md).

## Behind a load balancer

**No proxy is trusted by default: the client address is the socket's, and `X-Forwarded-For` is just a header.** A default that trusts nothing cannot need the security fix other frameworks shipped after release, when a client could forge its address or scheme ([ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md)). Behind a balancer, [`Request.RemoteAddr`](../reference/middleware.md#requestremoteaddr) is the balancer's address.

Name your balancers with [`WithTrustedProxies`](../reference/app.md#withtrustedproxies). From then on [`Request.ClientAddr`](../reference/middleware.md#requestclientaddr) is the client's address read from `X-Forwarded-For`, but only for a connection that comes from one of those networks, and [`Request.Scheme`](../reference/middleware.md#requestscheme) believes their `X-Forwarded-Proto`:

<!-- compiles: decls -->
```go
func newApp() *bites.App {
	return bites.New(bites.WithTrustedProxies(bites.TrustedProxies{
		Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, // the balancers' subnet
	}))
}

// clientIP logs the client's address on every request, as the app can trust it.
func clientIP(ctx context.Context, req bites.Request, next bites.Next) (bites.Response, error) {
	req.AddLogAttrs(slog.String("client_ip", req.ClientAddr().String()))
	return next.Call(ctx), nil
}
```

The header is read **from the right**: each proxy appends the address it saw, so the rightmost element was written by your nearest balancer and the leftmost by whoever sent the request first, which may be a client lying. The walk skips your own balancers and stops at the first address that is not one of them. A client that sends `X-Forwarded-For: 1.2.3.4` through your balancer arrives as `1.2.3.4, 198.51.100.9`, and `ClientAddr` is `198.51.100.9`, its real address; a client that talks to the app directly is its own address, whatever it sends.

- **Behind a CDN and a balancer**, list both ranges. If the CDN publishes a new range and your list is stale, `ClientAddr` is a CDN edge's address: clients behind that edge share a rate-limit bucket, and none of them can choose its own.
- **Behind a CDN that writes one address** (`CF-Connecting-IP`, `Fly-Client-IP`), give that as `Header`; it is read only from a trusted peer.
- **On Kubernetes**, trusting the ingress pods' range trusts every pod in it: a pod that calls the service directly can name any address. Trust the narrowest range you can.
- A prefix of length 0 (`0.0.0.0/0`) is refused: it would let every client name any address.

Rate limiting by address keys on `ClientAddr`, and HSTS is sent only when `Scheme` is `https` ([security](./security.md)).

**A link the client follows must name the host and path the client used, not the ones the proxy forwarded to.** A proxy that serves the app at `https://api.example.com/api` and forwards to `http://chat.internal:8080` without the `/api` makes a `Location: /rooms/7` point at `https://api.example.com/rooms/7`, which is not the app. Tell the app that your proxies write `X-Forwarded-Host` and `X-Forwarded-Prefix`, and build links with [`Request.PublicURL`](../reference/middleware.md#requestpublicurl) or [`Request.PublicPrefix`](../reference/middleware.md#requestpublicprefix), usually in a Provider ([ADR 0025](../adr/0025-no-proxy-is-trusted-by-default.md)):

<!-- compiles: decls -->
```go
// Links is the start of every link a handler writes for its client.
type Links struct{ Base string }

func newProxiedApp() *bites.App {
	app := bites.New(bites.WithTrustedProxies(bites.TrustedProxies{
		Prefixes:        []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		ForwardedHost:   true, // the ingress replaces X-Forwarded-Host with the host the client asked for
		ForwardedPrefix: true, // and writes X-Forwarded-Prefix: /api
	}))
	// Links{Base: "https://api.example.com/api"}; a handler returns Location: links.Base + "/rooms/7".
	app.Provide(func(ctx context.Context, req bites.Request, _ struct{}) (Links, error) {
		return Links{Base: req.PublicURL("")}, nil
	})
	return app
}
```

- **Turn on only what your proxy replaces.** Both are off by default, even for a trusted proxy, because a balancer that writes `X-Forwarded-For` may pass a client's `X-Forwarded-Host` through untouched, and a host the client chooses in a password-reset link sends the reset to the attacker. Behind a balancer that forwards the `Host` header unchanged, leave `ForwardedHost` off: `Host` is already the public one.
- **The nearest proxy's value is read**: the rightmost element of the last line. Behind two proxies, configure the nearest to pass the public host and the whole prefix on (`/api/v1`, not `/v1`).
- **A value that is not a host, or not a clean absolute path, is ignored**: `PublicHost` is then `Host` and `PublicPrefix` is empty. An IPv6 host must be in brackets (`[2001:db8::1]:8443`), and a prefix such as `//evil.example`, which a browser would read as another host, is refused.
- **`Forwarded` (RFC 7239) is not read**, and naming it as `Header` is refused: a proxy that writes one family of headers often passes the other through, so reading both would let the client write the one your proxy does not touch.
- **If the public URL is fixed**, you may keep it in your configuration instead and build links from it, as [`examples/chat`](../../examples/chat/README.md) does with `CHAT_PUBLIC_URL`; the [API document's `servers`](./openapi.md#serving-the-document) are always set that way. **A link that leaves the request, in an email or a push notification, is built from that configuration**: it is often sent from a job that has no request at all, and of the request's headers only `Host`, or the `X-Forwarded-Host` your proxy writes, names your app. `Origin` names the page that sent the request; it is read for a decision about that request (CSRF, a WebSocket's site) and is not the place to take a host from.

Two more things follow from this. A [WebSocket](./websockets.md#choosing-which-sites-may-open-a-socket) compares the `Origin` to the request's `Host`, and to the forwarded host when `ForwardedHost` is set, and never infers the scheme of the connection, so a TLS-terminating balancer that forwards or rewrites the host works without listing your own origin. And a balancer's idle timeout should be shorter than the app's `IdleTimeout` (60 seconds), so the balancer closes first ([limits](./limits.md#timeouts)).

## The PROXY protocol

**A TCP balancer (an AWS NLB, HAProxy, nginx's `proxy_protocol`, an ingress in TCP mode) cannot add `X-Forwarded-For`, because it never reads HTTP; it says who the client is with a PROXY protocol header at the start of the connection instead.** Give the listener it connects to [`ProxyProtocol()`](../reference/app.md#proxyprotocol), and name the balancer's networks with `WithTrustedProxies` as above ([ADR 0058](../adr/0058-the-proxy-protocol-is-turned-on-per-listener-and-accepted-only-from-a-trusted-proxy.md)):

<!-- compiles: decls -->
```go
func serveBehindNLB(app *bites.App) error {
	errs := make(chan error, 2)
	go func() { errs <- app.Listen(":8443", bites.ProxyProtocol()) }() // the NLB's target port
	go func() { errs <- app.Listen(":8080") }()                         // the kubelet's probes, no header
	return <-errs
}
```

The client the header names becomes the connection's peer: `Request.RemoteAddr` and `Request.ClientAddr` are the client's address, and a rate limit by address counts the client, not the balancer. Versions 1 and 2 are both read; a health check the balancer makes with a version 2 `LOCAL` header keeps the balancer as the peer.

- **It is per listener.** A balancer speaks it on the port it is configured for, and your orchestrator's probes reach another port without it: give that one its own listener, as above. One port cannot serve both.
- **Only a trusted peer may send it.** A connection from outside `Prefixes` is closed as soon as it is accepted, before anything it sent is read, so a client that reaches the port directly cannot name an address. A trusted peer that sends no header, or a broken one, is closed with nothing written; if every connection is closed, look for the Warn line `a trusted peer did not start its connection with a PROXY header` (the balancer is not set to send one) or `came from a peer that is not a trusted proxy` (your `Prefixes` is stale).
- **Timeouts.** The wait for the header is the connection's idle wait, so a balancer that sends it with the client's first request is fine; once begun, the header must arrive within `HeadTimeout`, or before the TLS handshake within `HandshakeTimeout`.
- **With TLS** the header comes first and the app terminates TLS after it: `app.ListenTLS(":8443", cfg, bites.ProxyProtocol())`.
- **On a Unix socket**, nginx with `proxy_protocol on` toward `ListenUnix(path, 0o660, bites.ProxyProtocol())` works when you set `UnixSockets: true` in `TrustedProxies`: a Unix peer has no address to list, so the socket's permission bits are the list. The same field makes `X-Forwarded-For` from a proxy on the Unix socket believed, without the PROXY protocol.

Without `WithTrustedProxies`, with only Unix peers trusted on a TCP listener, or on a Unix socket without `UnixSockets`, the serving call panics: no peer could send the header.

## Reading the clock

**[`WithClock(c)`](../reference/app.md#withclock) gives the app the clock its timeouts, access-log durations and [`Timeout`](./first-party-middleware.md#cutting-off-a-slow-handler) middleware measure on; it is for tests.** The default is real time, and [`bitetest.NewClock`](../reference/bitetest.md#bitetestnewclock) is a clock that moves only when told to ([ADR 0027](../adr/0027-an-in-memory-entry-point-runs-the-engine-path.md)).

## The start-up log

**When the app starts serving, it logs each setting on its own line, marked `default` or `custom`.** Read it once after a deploy to see what the process is really running with ([ADR 0026](../adr/0026-no-global-state-and-one-behaviour.md)).

```text
level=INFO msg="bites: settings" setting=naming source=default
level=INFO msg="bites: settings" setting="max body bytes" value=1048576 source=default
level=INFO msg="bites: settings" setting="write timeout" value=30s source=default
level=INFO msg="bites: settings" setting="max connections" value=471852 source=default
level=INFO msg="bites: settings" setting="idle connections park" value=on source=default
```

Each listener adds its own `bites: listening` line with its network, address, whether it is TLS and whether it reads the PROXY protocol. The settings lines cover the head, fields, body, timeouts, the TLS handshake, the effective connection limit (computed from the process's file descriptor limit, not the word "default"), JSON depth, parking, the trusted proxies, the codings and their minimum size when compression is on, the WebSocket limits when the app has a WebSocket route, and each upload route's limits. The [limits page](./limits.md) says what each one does.
