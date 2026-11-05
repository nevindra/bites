package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nevindra/bites"
)

func main() {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		log.Fatal("JWT_SECRET is required (32 bytes or more)")
	}
	app := newApp(Config{
		Store:     NewMemStore(),
		JWTSecret: []byte(secret),
		Origins:   []string{"http://localhost:5173"},
	})

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8081"
	}
	stop, cancelSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelSignals()
	served := make(chan error, 1)
	go func() { served <- app.Listen(addr) }()
	select {
	case err := <-served:
		log.Fatal(err)
	case <-stop.Done():
	}
	grace, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := app.Shutdown(grace); err != nil {
		log.Fatal(err)
	}
	if err := <-served; !errors.Is(err, bites.ErrServerClosed) {
		log.Fatal(err)
	}
}
