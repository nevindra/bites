// Command chat is the reference workload of bites (ADR 0002) as a program: a JSON API over
// Postgres with long-lived WebSockets. A message posted by HTTP or by a socket is inserted and
// published to the room's Topic, whose Broker (modules/pgnotify, Postgres LISTEN/NOTIFY) carries it
// to every instance, and each instance sends it to the sockets in the room. See README.md for how to run it.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/modules/pgnotify"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("chat: stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	// The framework installs no signal handler (ADR 0029): catching SIGTERM is the program's.
	stop, cancelSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelSignals()

	store, err := OpenPostgres(stop, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	// The broker publishes on the store's pool and holds one more connection for LISTEN. The
	// instance reports itself not ready while that connection is down, so a balancer sends new
	// sockets to an instance that hears (app is set before Run starts).
	var app *bites.App
	broker := pgnotify.New(pgnotify.Options{
		URL: cfg.DatabaseURL, Channel: "chat", Pool: store.pool, Log: log,
		OnState: func(listening bool) { app.SetReady(listening) },
	})
	defer broker.Close() // before store.Close, which closes the pool it publishes on
	hub := NewHub(log, broker)
	app = newApp(cfg, store, hub, log)
	app.SetReady(false) // until the LISTEN connection is up
	go broker.Run(stop)

	served := make(chan error, 1)
	go func() { served <- app.Listen(cfg.Addr) }()
	select {
	case err := <-served:
		return err // it never started
	case <-stop.Done():
	}

	log.Info("chat: shutting down")
	grace, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout+cfg.ShutdownDelay)
	defer cancel()
	if err := app.Shutdown(grace); err != nil {
		return err
	}
	if err := <-served; err != nil && !errors.Is(err, bites.ErrServerClosed) {
		return err
	}
	return nil
}
