package main

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nevindra/bites/bitetest"
)

func fakeCatalog(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/products/1":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":1,"name":"Lamp","sku":"LMP-0001","price":19.5}`)
		case "/products/500":
			http.Error(w, "boom", 500)
		default:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(404)
			fmt.Fprint(w, `{"title":"Not Found","status":404}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func start(t *testing.T) *bitetest.Server {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cat := fakeCatalog(t)
	return bitetest.Start(t, newApp(Config{CatalogURL: cat.URL, Invoices: root}))
}

type problem struct {
	Errors []struct{ Location, Message string }
}

func post(srv *bitetest.Server, target, body string) bitetest.Response {
	return srv.Do(bitetest.Request{Method: "POST", Target: target, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(body)})
}

func put(srv *bitetest.Server, target, body string) bitetest.Response {
	return srv.Do(bitetest.Request{Method: "PUT", Target: target, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(body)})
}

func newOrder(t *testing.T, srv *bitetest.Server) Order {
	t.Helper()
	r := post(srv, "/orders", `{"customer":"Ada","items":[{"product_id":1,"qty":2}]}`)
	if r.Status != 201 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	return bitetest.JSON[Order](t, r)
}

func TestCreatingAnOrderChecksEachProductWithCatalog(t *testing.T) {
	srv := start(t)
	o := newOrder(t, srv)
	if o.ID != 1 || o.Status != "pending" || len(o.Lines) != 1 || o.Lines[0].Price != 19.5 {
		t.Fatalf("%+v", o)
	}
}

func TestAProductCatalogDoesNotKnowIsRefusedNamingTheItem(t *testing.T) {
	srv := start(t)
	r := post(srv, "/orders", `{"customer":"Ada","items":[{"product_id":1,"qty":2},{"product_id":42,"qty":1}]}`)
	p := bitetest.JSON[struct {
		Errors []struct{ Location, Message string }
	}](t, r)
	if r.Status != 400 || len(p.Errors) != 1 || p.Errors[0].Location != "body.items[1].product_id" {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestACatalogFailureIs502(t *testing.T) {
	srv := start(t)
	r := post(srv, "/orders", `{"customer":"Ada","items":[{"product_id":500,"qty":2}]}`)
	if r.Status != 502 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestABadOrderBodyIsA400NamingEveryField(t *testing.T) {
	srv := start(t)
	r := post(srv, "/orders", `{"customer":"","items":[{"product_id":1,"qty":0}]}`)
	p := bitetest.JSON[problem](t, r)
	var locs []string
	for _, e := range p.Errors {
		locs = append(locs, e.Location)
	}
	if r.Status != 400 || strings.Join(locs, ",") != "body.customer,body.items[0].qty" {
		t.Fatalf("%d %v %s", r.Status, locs, r.Text())
	}
	r = post(srv, "/orders", `{"customer":"Ada","items":[]}`)
	if r.Status != 400 || !strings.Contains(r.Text(), "body.items") {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAMissingOrderIs404(t *testing.T) {
	srv := start(t)
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/orders/9"}); r.Status != 404 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	if r := put(srv, "/orders/9/status", `{"status":"paid"}`); r.Status != 404 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAStatusThatIsNotOneOfTheValuesIsA400(t *testing.T) {
	srv := start(t)
	newOrder(t, srv)
	r := put(srv, "/orders/1/status", `{"status":"lost"}`)
	if r.Status != 400 || !strings.Contains(r.Text(), "body.status") {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestAStatusChangeReachesTheSocketsWatchingTheOrder(t *testing.T) {
	srv := start(t)
	newOrder(t, srv)
	ws := srv.DialWS(bitetest.WSRequest{Target: "/orders/1/ws"})
	other := srv.DialWS(bitetest.WSRequest{Target: "/orders/1/ws"})

	if r := put(srv, "/orders/1/status", `{"status":"paid"}`); r.Status != 200 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	for _, c := range []*bitetest.WS{ws, other} {
		var got StatusChange
		c.ReadJSON(&got)
		if got.OrderID != 1 || got.Status != "paid" {
			t.Fatalf("%+v", got)
		}
	}
	ws.Close(1000, "bye")
	ws.ExpectClose(1000)
}

func TestWatchingAnOrderThatDoesNotExistIsRefusedBeforeTheUpgrade(t *testing.T) {
	srv := start(t)
	_, resp := srv.TryDialWS(bitetest.WSRequest{Target: "/orders/9/ws"})
	if resp.Status != 404 {
		t.Fatalf("%d %s", resp.Status, resp.Text())
	}
}

func TestTheEventStreamSendsAStatusChange(t *testing.T) {
	srv := start(t)
	newOrder(t, srv)
	events := srv.OpenEvents(bitetest.Request{Target: "/events"})
	if c := events.Next(); c.Comment != "open" { // sent once the stream is subscribed
		t.Fatalf("%+v", c)
	}
	put(srv, "/orders/1/status", `{"status":"shipped"}`)
	if ev := events.Next(); ev.Name != "status" || ev.Data != `{"order_id":1,"status":"shipped"}` {
		t.Fatalf("%+v", ev)
	}
}

func multipartBody(t *testing.T, field, filename string, content []byte) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(content)
	w.Close()
	return w.FormDataContentType(), buf.Bytes()
}

func TestAnInvoiceCanBeUploadedAndDownloadedAgain(t *testing.T) {
	srv := start(t)
	newOrder(t, srv)
	if r := srv.Do(bitetest.Request{Method: "GET", Target: "/orders/1/invoice"}); r.Status != 404 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	ct, body := multipartBody(t, "invoice", "inv.pdf", []byte("%PDF-1.4 hello"))
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/orders/1/invoice", Header: map[string][]string{"Content-Type": {ct}}, Body: body})
	if r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	r = srv.Do(bitetest.Request{Method: "GET", Target: "/orders/1/invoice"})
	if r.Status != 200 || r.Get("Content-Type") != "application/pdf" || string(r.Body) != "%PDF-1.4 hello" ||
		!strings.Contains(r.Get("Content-Disposition"), "invoice-1.pdf") {
		t.Fatalf("%d %v %q", r.Status, r.Header, r.Body)
	}
}

func TestAnUploadWithoutTheInvoicePartIsA400AndABigOneIsA413(t *testing.T) {
	srv := start(t)
	newOrder(t, srv)
	ct, body := multipartBody(t, "other", "x.txt", []byte("x"))
	r := srv.Do(bitetest.Request{Method: "POST", Target: "/orders/1/invoice", Header: map[string][]string{"Content-Type": {ct}}, Body: body})
	if r.Status != 400 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
	big := make([]byte, 6<<20)
	r = srv.Do(bitetest.Request{Method: "POST", Target: "/orders/1/invoice", Header: map[string][]string{"Content-Type": {ct}}, Body: big})
	if r.Status != 413 {
		t.Fatalf("%d %s", r.Status, r.Text())
	}
}

func TestHealth(t *testing.T) {
	srv := start(t)
	for _, p := range []string{"/livez", "/readyz"} {
		if r := srv.Do(bitetest.Request{Method: "GET", Target: p}); r.Status != 200 {
			t.Fatalf("%s %d", p, r.Status)
		}
	}
}
