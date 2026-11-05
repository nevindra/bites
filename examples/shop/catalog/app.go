package main

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/modules/docsui"
	"github.com/nevindra/bites/modules/jwt"
)

// ProductName is 2 to 80 characters.
type ProductName string

func (ProductName) Length() (min, max int) { return 2, 80 }

// SKU is three capital letters, a hyphen and four digits.
type SKU string

func (SKU) Pattern() string { return `^[A-Z]{3}-[0-9]{4}$` }

// Price is 0.01 to 100000.
type Price float64

func (Price) Range() (min, max float64) { return 0.01, 100000 }

// Claims are the claims of a token the catalog accepts.
type Claims struct {
	Subject string `json:"sub"`
	Scope   string `json:"scope"`
}

// Config is what the catalog needs from its environment.
type Config struct {
	Store     Store
	JWTSecret []byte
	Logger    *slog.Logger
	Origins   []string
}

// Products is the service whose methods are the handlers.
type Products struct {
	store Store
}

type ListProducts struct {
	Query struct {
		Page    bites.Opt[bites.PositiveInt]
		PerPage bites.Opt[bites.PositiveInt]
		Name    bites.Opt[string]
	}
}

type ProductList struct {
	Items   []Product
	Page    int
	PerPage int
	Total   int
}

func (s *Products) List(ctx context.Context, in ListProducts) (ProductList, error) {
	page := int(in.Query.Page.Or(1))
	per := int(in.Query.PerPage.Or(20))
	items, total, err := s.store.List(ctx, in.Query.Name.Or(""), (page-1)*per, per)
	if err != nil {
		return ProductList{}, err
	}
	return ProductList{Items: items, Page: page, PerPage: per, Total: total}, nil
}

type GetProduct struct {
	Path struct{ ID int64 }
}

func (s *Products) Get(ctx context.Context, in GetProduct) (Product, error) {
	p, err := s.store.Get(ctx, in.Path.ID)
	return p, mapErr(err)
}

type CreateProduct struct {
	Caller Claims
	Body   struct {
		Name  ProductName
		SKU   SKU
		Price Price
	}
}

func (s *Products) Create(ctx context.Context, in CreateProduct) (bites.Created[Product], error) {
	p, err := s.store.Create(ctx, Product{Name: string(in.Body.Name), SKU: string(in.Body.SKU), Price: float64(in.Body.Price)})
	if err != nil {
		return bites.Created[Product]{}, mapErr(err)
	}
	return bites.Created[Product]{Body: p, Location: "/products/" + itoa(p.ID)}, nil
}

type UpdateProduct struct {
	Caller Claims
	Path   struct{ ID int64 }
	Body   struct {
		Name  ProductName
		SKU   SKU
		Price Price
	}
}

func (s *Products) Update(ctx context.Context, in UpdateProduct) (Product, error) {
	p, err := s.store.Update(ctx, Product{ID: in.Path.ID, Name: string(in.Body.Name), SKU: string(in.Body.SKU), Price: float64(in.Body.Price)})
	return p, mapErr(err)
}

type DeleteProduct struct {
	Caller Claims
	Path   struct{ ID int64 }
}

func (s *Products) Delete(ctx context.Context, in DeleteProduct) (bites.NoContent, error) {
	return bites.NoContent{}, mapErr(s.store.Delete(ctx, in.Path.ID))
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return bites.NotFound("no such product")
	case errors.Is(err, ErrDuplicateSKU):
		return bites.Conflict("a product with this sku exists")
	}
	return err
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

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

	limiter := bites.NewRateLimiter(bites.RateLimitOptions{Limit: 600, Period: time.Minute, Burst: 100})
	app.Use(
		bites.RequestID(),
		bites.CORS(bites.CORSOptions{
			Origins: cfg.Origins,
			Methods: []string{"GET", "POST", "PUT", "DELETE"},
			Headers: []string{"Content-Type", "Authorization"},
		}),
		limiter.Middleware(),
	)

	verifier := jwt.New[Claims](jwt.Config{
		Keys:  jwt.Keys(jwt.HS256("k1", cfg.JWTSecret)),
		Clock: app.Clock(),
	})
	app.Provide(bites.BearerAuth(verifier.Verify))

	products := &Products{store: cfg.Store}
	app.OpenAPI("/openapi.json", bites.Info{Title: "Catalog", Version: "1.0.0"})
	docsui.Mount(app, "/docs", docsui.Config{SpecURL: "/openapi.json", Title: "Catalog API"})

	app.Get("/products", products.List, bites.Tag("products"))
	app.Get("/products/{id}", products.Get, bites.Tag("products"))
	app.Post("/products", products.Create, bites.Tag("products"))
	app.Put("/products/{id}", products.Update, bites.Tag("products"))
	app.Delete("/products/{id}", products.Delete, bites.Tag("products"))
	return app
}
