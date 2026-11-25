package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, 25*time.Second, func() error { return app.Listen(addr) }); err != nil {
		log.Fatal(err)
	}
}
