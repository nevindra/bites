// The chi server of the harness (github.com/go-chi/chi/v5): chi's router on net/http with no
// middleware, and the handler code of the nethttp server (strconv, encoding/json with unknown
// members refused), plus the same hand-written required checks as the other framework servers.
// It isolates what a popular router adds over ServeMux. A mistake is a plain 400.
package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nevindra/bites/bench/common"
)

func main() {
	flag.Parse()
	common.StartStats()
	r := chi.NewRouter()
	r.Post("/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "path.id: expected an integer", http.StatusBadRequest)
			return
		}
		q := r.URL.Query()
		page, err := strconv.Atoi(q.Get("page"))
		if err != nil {
			http.Error(w, "query.page: expected an integer", http.StatusBadRequest)
			return
		}
		if e := q.Get("expand"); e != "" {
			if _, err := strconv.ParseBool(e); err != nil {
				http.Error(w, "query.expand: expected a boolean", http.StatusBadRequest)
				return
			}
		}
		var body common.NewUser
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil || !body.Complete() {
			http.Error(w, "body: invalid", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(common.Created{ID: id, Name: body.Name, Email: body.Email, Age: body.Age, Page: page})
	})
	r.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "path.id: expected an integer", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(common.User{ID: id, Name: "ada", Email: "ada@example.dev", Age: 36})
	})
	r.Get("/block", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(common.BlockDelay)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(common.Blocked{OK: true})
	})
	if err := http.ListenAndServe(*common.Addr, r); err != nil {
		panic(err)
	}
}
