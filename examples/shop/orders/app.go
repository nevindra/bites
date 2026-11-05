package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/nevindra/bites"
)

// Status is where an order is in its life.
type Status string

func (Status) Values() []Status { return []Status{"pending", "paid", "shipped", "cancelled"} }

// Item is one line of an order.
type Item struct {
	ProductID bites.PositiveInt64
	Qty       bites.PositiveInt
}

// Items is 1 to 50 lines.
type Items []Item

func (Items) Items() (min, max int) { return 1, 50 }

// Order is an order as the wire shows it.
type Order struct {
	ID       int64
	Customer string
	Status   Status
	Lines    []OrderLine
	Invoice  string
}

// OrderLine is a priced line.
type OrderLine struct {
	ProductID int64
	Qty       int
	Price     float64
}

// StatusChange is what a subscriber receives.
type StatusChange struct {
	OrderID int64
	Status  Status
}

// Config is what the orders service needs.
type Config struct {
	CatalogURL string
	HTTP       *http.Client
	Invoices   *os.Root
	Logger     *slog.Logger
}

// Orders is the service whose methods are the handlers.
type Orders struct {
	cfg Config

	mu     sync.Mutex
	next   int64
	orders map[int64]*Order
	topics map[int64]*bites.Topic[StatusChange]
	subs   map[chan StatusChange]struct{}
}

func newOrders(cfg Config) *Orders {
	if cfg.HTTP == nil {
		cfg.HTTP = http.DefaultClient
	}
	return &Orders{cfg: cfg, next: 1, orders: map[int64]*Order{}, topics: map[int64]*bites.Topic[StatusChange]{}, subs: map[chan StatusChange]struct{}{}}
}

type catalogProduct struct {
	ID    int64   `json:"id"`
	Price float64 `json:"price"`
}

// lookup asks catalog for a product: (price, found, error).
func (s *Orders) lookup(ctx context.Context, id int64) (float64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.CatalogURL+"/products/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return 0, false, err
	}
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var p catalogProduct
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			return 0, false, err
		}
		return p.Price, true, nil
	case http.StatusNotFound:
		return 0, false, nil
	}
	return 0, false, fmt.Errorf("catalog answered %d", resp.StatusCode)
}

type CreateOrder struct {
	Body struct {
		Customer bites.NonEmpty
		Items    Items
	}
}

func (s *Orders) Create(ctx context.Context, in CreateOrder) (bites.Created[Order], error) {
	var lines []OrderLine
	for i, it := range in.Body.Items {
		price, ok, err := s.lookup(ctx, int64(it.ProductID))
		switch {
		case err != nil:
			return bites.Created[Order]{}, bites.NewError(http.StatusBadGateway, "catalog is not answering")
		case !ok:
			return bites.Created[Order]{}, bites.Invalid(fmt.Sprintf("body.items[%d].product_id", i), "names no product in the catalog")
		}
		lines = append(lines, OrderLine{ProductID: int64(it.ProductID), Qty: int(it.Qty), Price: price})
	}
	s.mu.Lock()
	o := &Order{ID: s.next, Customer: string(in.Body.Customer), Status: "pending", Lines: lines}
	s.next++
	s.orders[o.ID] = o
	s.mu.Unlock()
	return bites.Created[Order]{Body: *o, Location: "/orders/" + strconv.FormatInt(o.ID, 10)}, nil
}

type GetOrder struct {
	Path struct{ ID int64 }
}

func (s *Orders) get(id int64) (*Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[id]
	if !ok {
		return nil, bites.NotFound("no such order")
	}
	return o, nil
}

func (s *Orders) Get(ctx context.Context, in GetOrder) (Order, error) {
	o, err := s.get(in.Path.ID)
	if err != nil {
		return Order{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return *o, nil
}

type SetStatus struct {
	Path struct{ ID int64 }
	Body struct{ Status Status }
}

func (s *Orders) topic(id int64) *bites.Topic[StatusChange] {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.topics[id]
	if !ok {
		t = &bites.Topic[StatusChange]{}
		s.topics[id] = t
	}
	return t
}

func (s *Orders) SetStatus(ctx context.Context, in SetStatus) (Order, error) {
	o, err := s.get(in.Path.ID)
	if err != nil {
		return Order{}, err
	}
	s.mu.Lock()
	o.Status = in.Body.Status
	out := *o
	ch := StatusChange{OrderID: o.ID, Status: o.Status}
	for sub := range s.subs {
		select {
		case sub <- ch:
		default: // a slow event stream loses events rather than holding the others
		}
	}
	s.mu.Unlock()
	if err := s.topic(o.ID).Publish(ch); err != nil {
		return Order{}, err
	}
	return out, nil
}

// Watch is the WebSocket route. The socket only sends, so its message type is struct{} and its
// Inbox has no OnMessage: a client that sends anything is closed with 1003.
type Watch struct {
	Path struct{ ID int64 }
}

func (s *Orders) Watch(ctx context.Context, in Watch, sock *bites.Socket[StatusChange]) (bites.Inbox[struct{}], error) {
	if _, err := s.get(in.Path.ID); err != nil {
		return bites.Inbox[struct{}]{}, err
	}
	s.topic(in.Path.ID).Subscribe(sock)
	return bites.Inbox[struct{}]{}, nil
}

func (s *Orders) Events(ctx context.Context, in struct{}) (bites.Events[StatusChange], error) {
	return bites.Events[StatusChange]{Func: func(ctx context.Context, e bites.EventSender[StatusChange]) error {
		ch := make(chan StatusChange, 16)
		s.mu.Lock()
		s.subs[ch] = struct{}{}
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.subs, ch)
			s.mu.Unlock()
		}()
		if err := e.Comment("open"); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case c := <-ch:
				if err := e.Send("status", c); err != nil {
					return err
				}
			}
		}
	}}, nil
}

type UploadInvoice struct {
	Path struct{ ID int64 }
	Body bites.Upload
}

func (s *Orders) UploadInvoice(ctx context.Context, in UploadInvoice) (bites.NoContent, error) {
	o, err := s.get(in.Path.ID)
	if err != nil {
		return bites.NoContent{}, err
	}
	name := "invoice-" + strconv.FormatInt(o.ID, 10) + ".pdf"
	found := false
	err = in.Body.Each(func(part bites.Part) error {
		if part.Name != "invoice" {
			return nil
		}
		f, err := s.cfg.Invoices.Create(name)
		if err != nil {
			return err
		}
		defer f.Close()
		found = true
		_, err = io.Copy(f, part)
		return err
	})
	if err != nil {
		return bites.NoContent{}, err
	}
	if !found {
		return bites.NoContent{}, bites.BadRequest("the upload has no part named invoice")
	}
	s.mu.Lock()
	o.Invoice = name
	s.mu.Unlock()
	return bites.NoContent{}, nil
}

func (s *Orders) DownloadInvoice(ctx context.Context, in GetOrder) (bites.File, error) {
	o, err := s.get(in.Path.ID)
	if err != nil {
		return bites.File{}, err
	}
	s.mu.Lock()
	name := o.Invoice
	s.mu.Unlock()
	if name == "" {
		return bites.File{}, bites.NotFound("this order has no invoice")
	}
	return bites.File{Root: s.cfg.Invoices, Name: name, ContentType: "application/pdf", Attachment: name}, nil
}

// newApp registers every route; main and the tests both call it.
func newApp(cfg Config) *bites.App {
	opts := []bites.Option{
		bites.WithObserver(bites.AccessLog()),
		bites.WithHealth(bites.Health{Live: "/livez", Ready: "/readyz"}),
	}
	if cfg.Logger != nil {
		opts = append(opts, bites.WithLogger(cfg.Logger))
	}
	app := bites.New(opts...)
	app.Use(bites.RequestID())
	orders := newOrders(cfg)
	app.Post("/orders", orders.Create)
	app.Get("/orders/{id}", orders.Get)
	app.Put("/orders/{id}/status", orders.SetStatus)
	app.WebSocket("/orders/{id}/ws", orders.Watch)
	app.Get("/events", orders.Events)
	app.Post("/orders/{id}/invoice", orders.UploadInvoice, bites.MaxBody(5<<20))
	app.Get("/orders/{id}/invoice", orders.DownloadInvoice)
	return app
}
