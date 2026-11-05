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
