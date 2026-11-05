# shop: two services that talk to each other

A catalog service and an orders service, each its own program, sharing nothing but HTTP. It shows what a split into services asks of bites: a token verified by the [`jwt` module](../../docs/reference/jwt.md), a located 400 a handler returns after asking another service, a WebSocket that only sends, a stream of events, an upload and its download, and a test for each. It is its own Go module (its `go.mod` replaces the framework and the modules with the folders beside it, [ADR 0070](../../docs/adr/0070-optional-features-are-modules-under-modules-each-with-its-own-go-mod.md)).

It began as the application an agent built in the usability test of [ADR 0077](../../docs/adr/0077-four-principles-in-the-order-they-win.md)'s first principle ([`docs/research/agent-usability/`](../../docs/research/agent-usability/report.md)), and was changed to use what that test found missing. The stores are in memory, so it runs with nothing else installed.

## What it does

**catalog** (`:8081`):

- `GET /products?page=&per_page=&name=` lists products, `GET /products/{id}` reads one.
- `POST /products`, `PUT /products/{id}` and `DELETE /products/{id}` need `Authorization: Bearer <token>`, an HS256 token signed with `JWT_SECRET`. A name is 2 to 80 characters, a SKU is `ABC-1234`, a price is 0.01 to 100000; a second product with one SKU is a 409.
- Request ID, CORS for `http://localhost:5173`, a rate limit, the access log, `/livez` and `/readyz`, `/openapi.json` and the API documentation at `/docs`.

**orders** (`:8082`):

- `POST /orders` takes a customer and 1 to 50 items, and asks catalog for each product's price; a product catalog does not have is a 400 naming `body.items[i].product_id`, and a catalog that does not answer is a 502.
- `GET /orders/{id}`, and `PUT /orders/{id}/status` with one of `pending`, `paid`, `shipped`, `cancelled`.
- `GET /orders/{id}/ws` is a WebSocket that receives each status change of the order and takes no messages.
- `GET /events` is a Server-Sent Events stream of every status change.
- `POST /orders/{id}/invoice` takes a multipart upload with a part named `invoice` (5 MiB at most), and `GET /orders/{id}/invoice` downloads it.

## Run it

```sh
cd examples/shop
JWT_SECRET=a-secret-of-at-least-32-bytes-long go run ./catalog &
CATALOG_URL=http://localhost:8081 go run ./orders
```

## Test it

```sh
cd examples/shop
go test -race ./...
```

The tests run each service over `bitetest`'s in-memory connections. The orders tests put a fake catalog behind `httptest`; the catalog tests sign their tokens with `jwttest.Token`, and the stream is read with `OpenEvents`.
