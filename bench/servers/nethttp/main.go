// The net/http baseline of the harness: ServeMux patterns, PathValue, strconv and the
// encoding/json decoder and encoder, the code a Go developer writes without a framework (the
// same baseline as docs/research/std-huma). Validation is what that code does by hand: the
// path id, the page and the expand flag are parsed and a failure is a 400, the body must be
// well-formed JSON with no unknown member. It does NOT notice a missing body member, and its
// errors are plain text, not problem+json: the weakest validation of the four, so the cheapest.
package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"strconv"
	"time"

	"github.com/nevindra/bites/bench/common"
)

func main() {
	flag.Parse()
	common.StartStats()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
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
		if err := dec.Decode(&body); err != nil {
			http.Error(w, "body: "+err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(common.Created{ID: id, Name: body.Name, Email: body.Email, Age: body.Age, Page: page})
	})
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "path.id: expected an integer", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(common.User{ID: id, Name: "ada", Email: "ada@example.dev", Age: 36})
	})
	// GET /block: a handler that waits 10 ms (the block experiment).
	mux.HandleFunc("GET /block", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(common.BlockDelay)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(common.Blocked{OK: true})
	})
	if err := http.ListenAndServe(*common.Addr, mux); err != nil {
		panic(err)
	}
}
