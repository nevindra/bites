package bites_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

// metricsApp serves an app observed by m, with the metrics at /metrics.
func metricsApp(t *testing.T, m *bites.Metrics, register func(*bites.App, *bitetest.Clock)) (*bitetest.Server, *bitetest.Clock) {
	t.Helper()
	clk := bitetest.NewClock()
	app := bites.New(bites.WithClock(clk), bites.WithObserver(m))
	app.ServeMetrics("/metrics", m)
	register(app, clk)
	return bitetest.Start(t, app), clk
}

// scrape reads the metrics once the requests before it have ended.
func scrape(t *testing.T, srv *bitetest.Server, want string) string {
	t.Helper()
	var text string
	waitFor(t, "the metrics to show "+want, func() bool {
		r := srv.Do(req("GET", "/metrics"))
		if r.Status != 200 || r.Get("Content-Type") != bites.MetricsContentType {
			t.Fatalf("%d %v", r.Status, r.Header)
		}
		text = r.Text()
		return strings.Contains(text, want)
	})
	return text
}

func TestMetricsWriteAHistogramAndByteCountersPerMethodRouteAndCode(t *testing.T) {
	m := bites.NewMetrics(bites.MetricsOptions{Buckets: []time.Duration{100 * time.Millisecond, time.Second}})
	srv, _ := metricsApp(t, m, func(app *bites.App, clk *bitetest.Clock) {
		app.Post("/users/{id}", func(ctx context.Context, in struct {
			Path struct{ ID int64 }
			Body struct{ Name string }
		}) (User, error) {
			clk.Advance(250 * time.Millisecond)
			return User{ID: in.Path.ID, Name: in.Body.Name}, nil
		})
	})
	body := `{"name":"ada"}`
	r := srv.Do(jsonReq("POST", "/users/7", body))
	text := scrape(t, srv, `http_server_request_duration_seconds_count{method="POST"`)
	lines := []string{
		`http_server_request_duration_seconds_bucket{method="POST",route="/users/{id}",code="200",le="0.1"} 0`,
		`http_server_request_duration_seconds_bucket{method="POST",route="/users/{id}",code="200",le="1"} 1`,
		`http_server_request_duration_seconds_bucket{method="POST",route="/users/{id}",code="200",le="+Inf"} 1`,
		`http_server_request_duration_seconds_sum{method="POST",route="/users/{id}",code="200"} 0.25`,
		`http_server_request_duration_seconds_count{method="POST",route="/users/{id}",code="200"} 1`,
		fmt.Sprintf(`http_server_request_body_bytes_total{method="POST",route="/users/{id}",code="200"} %d`, len(body)),
		fmt.Sprintf(`http_server_response_body_bytes_total{method="POST",route="/users/{id}",code="200"} %d`, len(r.Body)),
		`http_server_abandoned_requests_total{method="POST",route="/users/{id}"} 0`,
		"# TYPE http_server_request_duration_seconds histogram",
		"# TYPE http_server_requests_active gauge",
		"bites_metrics_dropped_total 0",
	}
	for _, l := range lines {
		if !strings.Contains(text, l+"\n") {
			t.Errorf("missing %s\n%s", l, text)
		}
	}
}

func TestA404AnyPathAndAnUnknownMethodLandInOneSeriesWhateverAHostileClientSends(t *testing.T) {
	m := bites.NewMetrics()
	srv, _ := metricsApp(t, m, func(app *bites.App, _ *bitetest.Clock) { app.Get("/users/{id}", getUser) })
	for i := range 200 {
		srv.Do(req("GET", fmt.Sprintf("/probe/%d/wp-login.php", i)))
		srv.Do(req(fmt.Sprintf("X%d", i), "/users/1"))
	}
	text := scrape(t, srv, `http_server_request_duration_seconds_count{method="OTHER",route="",code="405"} 200`)
	if !strings.Contains(text, `http_server_request_duration_seconds_count{method="GET",route="",code="404"} 200`) {
		t.Fatalf("%s", text)
	}
	if n := strings.Count(text, "_count{"); n > 4 {
		t.Fatalf("%d series for two kinds of hostile request:\n%s", n, text)
	}
	if strings.Contains(text, "wp-login") || strings.Contains(text, "X1") {
		t.Fatalf("client bytes reached a label:\n%s", text)
	}
}

func TestMaxSeriesBoundsTheSeriesAndCountsWhatItDropped(t *testing.T) {
	m := bites.NewMetrics(bites.MetricsOptions{MaxSeries: 2})
	srv, _ := metricsApp(t, m, func(app *bites.App, _ *bitetest.Clock) {
		app.Get("/a", ok[Empty])
		app.Get("/b", ok[Empty])
		app.Get("/c", ok[Empty])
	})
	for _, p := range []string{"/a", "/b", "/c", "/c"} {
		srv.Do(req("GET", p))
	}
	// /c twice, and every scrape, which is a third label set too. Each End runs after its answer,
	// so the count is read until it has caught up.
	var text string
	waitFor(t, "the drops to be counted", func() bool {
		text = srv.Do(req("GET", "/metrics")).Text()
		var dropped int
		fmt.Sscanf(text[strings.LastIndex(text, "bites_metrics_dropped_total "):], "bites_metrics_dropped_total %d", &dropped)
		return dropped >= 2 && strings.Count(text, "_count{") == 2
	})
	if strings.Contains(text, `route="/c"`) {
		t.Fatalf("%s", text)
	}
}

func TestAnAbandonedRequestIsCountedAsAbandonedAndNotInTheHistogram(t *testing.T) {
	m := bites.NewMetrics()
	started := make(chan struct{})
	srv, _ := metricsApp(t, m, func(app *bites.App, _ *bitetest.Clock) {
		app.Get("/slow", func(ctx context.Context, _ Empty) (Empty, error) {
			close(started)
			<-ctx.Done()
			return Empty{}, ctx.Err()
		})
	})
	c := srv.Dial("203.0.113.9:1")
	fmt.Fprint(c, "GET /slow HTTP/1.1\r\nHost: x\r\n\r\n")
	<-started
	c.Close()
	text := scrape(t, srv, `http_server_abandoned_requests_total{method="GET",route="/slow"} 1`)
	if strings.Contains(text, `_count{method="GET",route="/slow"`) {
		t.Fatalf("%s", text)
	}
}

func TestTheActiveGaugeCountsARequestWhileItRuns(t *testing.T) {
	m := bites.NewMetrics()
	release := make(chan struct{})
	srv, _ := metricsApp(t, m, func(app *bites.App, _ *bitetest.Clock) {
		app.Get("/wait", func(context.Context, Empty) (Empty, error) { <-release; return Empty{}, nil })
	})
	done := make(chan struct{})
	go func() { srv.Do(req("GET", "/wait")); close(done) }()
	scrape(t, srv, "http_server_requests_active 2\n") // the waiting request and the scrape itself
	close(release)
	<-done
	scrape(t, srv, "http_server_requests_active 1\n")
}

func TestALabelValueIsEscapedAsTheTextFormatRequires(t *testing.T) {
	m := bites.NewMetrics()
	srv, _ := metricsApp(t, m, func(app *bites.App, _ *bitetest.Clock) {
		app.Get(`/a"b\c`, ok[Empty])
	})
	srv.Do(req("GET", `/a"b\c`))
	scrape(t, srv, `route="/a\"b\\c"`)
}

func TestWriteToWritesTheSameTextTheRouteServes(t *testing.T) {
	m := bites.NewMetrics()
	var b strings.Builder
	n, err := m.WriteTo(&b)
	if err != nil || int(n) != b.Len() || !strings.Contains(b.String(), "http_server_requests_active 0\n") {
		t.Fatalf("%d %v %q", n, err, b.String())
	}
}
