package main

import (
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
	"github.com/nevindra/bites/modules/jwt/jwttest"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func mint(t testing.TB, exp time.Time) string {
	t.Helper()
	return jwttest.Token(t, secret, "k1", map[string]any{"sub": "ada", "scope": "write", "exp": exp.Unix()})
}

func start(t *testing.T) (*bitetest.Server, struct{}) {
	t.Helper()
	app := newApp(Config{Store: NewMemStore(), JWTSecret: secret, Origins: []string{"http://localhost:5173"}})
	srv := bitetest.Start(t, app)
	return srv, struct{}{}
}

type problem struct {
	Status int
	Detail string
	Errors []struct{ Location, Message string }
}

func authed(t testing.TB, method, target, body string) bitetest.Request {
	r := bitetest.Request{Method: method, Target: target, Header: map[string][]string{
		"Authorization": {"Bearer " + mint(t, time.Now().Add(time.Hour))},
		"Content-Type":  {"application/json"},
	}}
	if body != "" {
		r.Body = []byte(body)
	}
	return r
}

func TestTheAppRegisters(t *testing.T) {
	start(t)
}

func create(t *testing.T, srv *bitetest.Server, body string) bitetest.Response {
	t.Helper()
	return srv.Do(authed(t, "POST", "/products", body))
}

func TestCreatingAProductAnswers201WithALocation(t *testing.T) {
	srv, _ := start(t)
	resp := create(t, srv, `{"name":"Lamp","sku":"LMP-0001","price":19.5}`)
	if resp.Status != 201 || resp.Get("Location") != "/products/1" {
		t.Fatalf("%d %s %v", resp.Status, resp.Text(), resp.Header)
	}
	got := bitetest.JSON[Product](t, resp)
	if got.ID != 1 || got.SKU != "LMP-0001" {
		t.Fatalf("%+v", got)
	}
}

func TestABadSkuNameAndPriceAreAll400sNamingTheirFields(t *testing.T) {
	srv, _ := start(t)
	resp := create(t, srv, `{"name":"L","sku":"lmp1","price":0}`)
	if resp.Status != 400 {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
	p := bitetest.JSON[problem](t, resp)
	var locs []string
	for _, e := range p.Errors {
		locs = append(locs, e.Location)
	}
	if strings.Join(locs, ",") != "body.name,body.sku,body.price" {
		t.Fatalf("%v: %s", locs, resp.Text())
	}
}

func TestAWriteWithoutATokenIs401(t *testing.T) {
	srv, _ := start(t)
	resp := srv.Do(bitetest.Request{Method: "POST", Target: "/products",
		Header: map[string][]string{"Content-Type": {"application/json"}},
		Body:   []byte(`{"name":"Lamp","sku":"LMP-0001","price":19.5}`)})
	if resp.Status != 401 || !strings.HasPrefix(resp.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("%d %s %v", resp.Status, resp.Text(), resp.Header)
	}
}

func TestAnExpiredTokenIs401InvalidToken(t *testing.T) {
	srv, _ := start(t)
	r := authed(t, "DELETE", "/products/1", "")
	r.Header["Authorization"] = []string{"Bearer " + mint(t, time.Now().Add(-time.Hour))}
	resp := srv.Do(r)
	if resp.Status != 401 || !strings.Contains(resp.Get("WWW-Authenticate"), "invalid_token") {
		t.Fatalf("%d %s %v", resp.Status, resp.Text(), resp.Header)
	}
}

func TestAMissingProductIs404(t *testing.T) {
	srv, _ := start(t)
	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/products/99"})
	if resp.Status != 404 {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
}

func TestAProductIdThatIsNotANumberIsA400NamingPathId(t *testing.T) {
	srv, _ := start(t)
	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/products/abc"})
	p := bitetest.JSON[problem](t, resp)
	if resp.Status != 400 || len(p.Errors) != 1 || p.Errors[0].Location != "path.id" {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
}

func TestADuplicateSkuIs409(t *testing.T) {
	srv, _ := start(t)
	body := `{"name":"Lamp","sku":"LMP-0001","price":19.5}`
	if r := create(t, srv, body); r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := create(t, srv, body); r.Status != 409 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestListingPagesAndFiltersByName(t *testing.T) {
	srv, _ := start(t)
	for _, b := range []string{
		`{"name":"Desk lamp","sku":"LMP-0001","price":10}`,
		`{"name":"Floor lamp","sku":"LMP-0002","price":20}`,
		`{"name":"Chair","sku":"CHR-0001","price":30}`,
	} {
		create(t, srv, b)
	}
	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/products?per_page=1&page=2"})
	list := bitetest.JSON[ProductList](t, resp)
	if resp.Status != 200 || list.Total != 3 || len(list.Items) != 1 || list.Items[0].SKU != "LMP-0002" {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
	list = bitetest.JSON[ProductList](t, srv.Do(bitetest.Request{Method: "GET", Target: "/products?name=lamp"}))
	if list.Total != 2 {
		t.Fatalf("%+v", list)
	}
	resp = srv.Do(bitetest.Request{Method: "GET", Target: "/products?page=0"})
	if resp.Status != 400 || !strings.Contains(resp.Text(), "query.page") {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
}

func TestUpdatingAndDeletingAProduct(t *testing.T) {
	srv, _ := start(t)
	create(t, srv, `{"name":"Lamp","sku":"LMP-0001","price":19.5}`)
	create(t, srv, `{"name":"Chair","sku":"CHR-0001","price":30}`)

	resp := srv.Do(authed(t, "PUT", "/products/1", `{"name":"Lamp 2","sku":"LMP-0001","price":25}`))
	if resp.Status != 200 || bitetest.JSON[Product](t, resp).Name != "Lamp 2" {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
	if r := srv.Do(authed(t, "PUT", "/products/1", `{"name":"Lamp 2","sku":"CHR-0001","price":25}`)); r.Status != 409 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := srv.Do(authed(t, "PUT", "/products/9", `{"name":"Lamp 2","sku":"ZZZ-0001","price":25}`)); r.Status != 404 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := srv.Do(authed(t, "DELETE", "/products/1", "")); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/products/1"}); r.Status != 404 {
		t.Fatalf("%d", r.Status)
	}
}

func TestEveryAnswerCarriesARequestIDAndCORSForAListedOrigin(t *testing.T) {
	srv, _ := start(t)
	resp := srv.Do(bitetest.Request{Method: "GET", Target: "/products", Header: map[string][]string{"Origin": {"http://localhost:5173"}}})
	if resp.Get("X-Request-ID") == "" || resp.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("%v", resp.Header)
	}
	pre := srv.Do(bitetest.Request{Method: "OPTIONS", Target: "/products", Header: map[string][]string{
		"Origin": {"http://localhost:5173"}, "Access-Control-Request-Method": {"POST"}, "Access-Control-Request-Headers": {"authorization"}}})
	if pre.Status != 204 || pre.Get("Access-Control-Allow-Headers") == "" {
		t.Fatalf("%d %v", pre.Status, pre.Header)
	}
}

func TestTheRateLimiterAnswers429WhenTheBurstIsSpent(t *testing.T) {
	srv, _ := start(t)
	var last bitetest.Response
	for range 150 {
		last = srv.Do(bitetest.Request{Method: "GET", Target: "/products"})
	}
	if last.Status != 429 || last.Get("Retry-After") == "" {
		t.Fatalf("%d %s", last.Status, last.Text())
	}
}

func TestHealthReadinessTheDocumentAndTheDocsPage(t *testing.T) {
	srv, _ := start(t)
	for _, path := range []string{"/livez", "/readyz", "/openapi.json", "/docs"} {
		if r := srv.Do(bitetest.Request{Method: "GET", Target: path}); r.Status != 200 {
			t.Fatalf("%s: %d %s", path, r.Status, r.Text())
		}
	}
	doc := srv.Do(bitetest.Request{Method: "GET", Target: "/openapi.json"}).Text()
	if !strings.Contains(doc, "bearerAuth") || !strings.Contains(doc, "/products/{id}") {
		t.Fatalf("%s", doc)
	}
}

var _ = bites.New
