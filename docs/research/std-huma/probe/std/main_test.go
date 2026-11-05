package main
import ("fmt";"log";"net/http";"net/http/httptest";"testing";"io";"os";"bytes";"time")
func rec(h http.Handler, method, p string) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, p, nil))
	b,_ := io.ReadAll(w.Body)
	fmt.Printf("%s %s -> %d hdr=%v body=%q\n", method, p, w.Code, w.Header(), string(b))
}
func TestConflict(t *testing.T) {
	for _, pair := range [][2]string{{"GET /a/{x}","GET /{y}/b"},{"/a/{x}","/a/{y}"},{"GET /a/{$}","GET /a/{$}"},{"/a/{x...}/b","/z"}, {"/{x}/a","/a/{y}"}} {
		func(){
			defer func(){ fmt.Printf("%q + %q => panic: %v\n", pair[0],pair[1], recover()) }()
			m := http.NewServeMux(); h := func(http.ResponseWriter,*http.Request){}
			m.HandleFunc(pair[0],h); m.HandleFunc(pair[1],h)
		}()
	}
}
func TestRouting(t *testing.T) {
	m := http.NewServeMux()
	h := func(w http.ResponseWriter, r *http.Request){ fmt.Fprint(w,"ok ", r.Pattern, " id=", r.PathValue("id")) }
	m.HandleFunc("GET /users/{id}", h)
	m.HandleFunc("GET /dir/", h)
	m.HandleFunc("GET /exact/{$}", h)
	rec(m,"POST","/users/1")
	rec(m,"GET","/users/1/")
	rec(m,"GET","/dir")
	rec(m,"GET","/exact")
	rec(m,"GET","/exact/")
	rec(m,"HEAD","/users/1")
	rec(m,"GET","/nope")
	rec(m,"GET","//users/1")
}
func TestFootguns(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){
		switch r.URL.Path {
		case "/a": w.Write([]byte("hi")); w.WriteHeader(500)
		case "/b": w.Write([]byte("hi")); w.Header().Set("X-Late","1")
		case "/c": http.Error(w, "bad", 400); w.Write([]byte("ALSO"))
		case "/d": w.Header().Set("X-Late","1"); w.Write([]byte("hi")); w.Header().Set("Trailer-ish","2")
		}
	}))
	srv.Config.ErrorLog = log.New(&buf, "srv: ", 0)
	srv.Start(); defer srv.Close()
	for _, p := range []string{"/a","/b","/c"} {
		resp, err := http.Get(srv.URL+p); if err!=nil {t.Fatal(err)}
		b,_ := io.ReadAll(resp.Body); resp.Body.Close()
		fmt.Printf("%s -> %d %v body=%q X-Late=%q\n", p, resp.StatusCode, resp.Header.Get("Content-Type"), b, resp.Header.Get("X-Late"))
	}
	time.Sleep(100*time.Millisecond)
	fmt.Println("LOG:", buf.String())
	_ = os.Stdout
}
func TestCtx(t *testing.T) {
	done := make(chan error,1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request){
		w.(http.Flusher).Flush()
		select { case <-r.Context().Done(): done <- context_cause(r) ; case <-time.After(3*time.Second): done <- nil }
	}))
	defer srv.Close()
	c := &http.Client{}
	req,_ := http.NewRequest("GET", srv.URL, nil)
	ctx, cancel := ctxWT(); defer cancel(); req = req.WithContext(ctx)
	c.Do(req)
	fmt.Println("server saw:", <-done)
}
