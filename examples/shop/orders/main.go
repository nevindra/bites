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
	dir := os.Getenv("INVOICE_DIR")
	if dir == "" {
		dir = "invoices"
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		log.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		log.Fatal(err)
	}
	catalog := os.Getenv("CATALOG_URL")
	if catalog == "" {
		catalog = "http://localhost:8081"
	}
	app := newApp(Config{CatalogURL: catalog, Invoices: root})

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8082"
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, 25*time.Second, func() error { return app.Listen(addr) }); err != nil {
		log.Fatal(err)
	}
}
