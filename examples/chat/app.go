package main

import (
	"log/slog"
	"time"

	"github.com/nevindra/bites"
)

// newApp builds the app without serving it, so a test builds the router (ADR 0007) and drives it
// over memory (ADR 0027). The Store and the Hub are given, so a test passes a fake.
func newApp(cfg Config, store Store, hub *Hub, log *slog.Logger) *bites.App {
	metrics := bites.NewMetrics()
	opts := []bites.Option{
		bites.WithLogger(log),
		// The access log and the metrics are Observers: they see the real status of every request,
		// a stream or a panic included, after the last byte (ADR 0051).
		bites.WithObserver(bites.AccessLog(), metrics),
		// A request with a traceparent joins its trace; one without begins one, so each log line
		// has a trace id.
		bites.WithTraceContext(bites.TraceContext{Begin: true}),
		// /livez says the process is not wedged, /readyz says it wants traffic. Readiness here
		// follows the LISTEN connection (see main.go), and goes 503 as Shutdown starts.
		bites.WithHealth(bites.Health{Live: "/livez", Ready: "/readyz"}),
		// After Shutdown the app keeps serving for this long with /readyz failing, so a balancer
		// stops sending traffic before the listeners close.
		bites.WithShutdownDelay(cfg.ShutdownDelay),
	}
	if len(cfg.TrustedProxies) > 0 {
		// Without this, every client's address is the balancer's, and the rate limit below would
		// count all clients as one.
		opts = append(opts, bites.WithTrustedProxies(bites.TrustedProxies{Prefixes: cfg.TrustedProxies}))
	}
	app := bites.New(opts...)

	limiter := bites.NewRateLimiter(bites.RateLimitOptions{
		Limit: cfg.RateLimitPerMinute, Period: time.Minute, Burst: max(cfg.RateLimitPerMinute/6, 1),
	})
	app.Use(bites.RequestID(), bites.SecurityHeaders(), limiter.Middleware())

	// The auth Provider: an Input with a User field is refused with a 401 before its handler runs.
	app.Provide(toyTokens())

	chat := &Chat{store: store, hub: hub, prefix: cfg.Prefix()}
	app.Post("/rooms", chat.CreateRoom, bites.Summary("Create a room"), bites.Tag("rooms"))
	app.Get("/rooms", chat.ListRooms, bites.Summary("List rooms, a page at a time"), bites.Tag("rooms"))
	app.Post("/rooms/{id}/messages", chat.PostMessage, bites.Summary("Post a message"), bites.Tag("messages"))
	app.Get("/rooms/{id}/messages", chat.History, bites.Summary("Read a room's history, newest first"), bites.Tag("messages"))
	app.WebSocket("/rooms/{id}/ws", chat.Join)

	app.ServeMetrics("/metrics", metrics)
	info := bites.Info{
		Title:       "Chat",
		Version:     "1.0.0",
		Description: "The bites example: a JSON API over Postgres, and a WebSocket per room fed through a Broker over LISTEN/NOTIFY. The auth is a toy.",
	}
	if cfg.PublicURL != "" {
		// The document is fixed bytes, the same for every request, so where the API is served is
		// configuration, not something read from a request (ADR 0025).
		info.Servers = []bites.Server{{URL: cfg.PublicURL}}
	}
	app.OpenAPI("/openapi.json", info)
	return app
}
