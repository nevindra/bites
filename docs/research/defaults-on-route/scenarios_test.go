package defaultsproto

import (
	"testing"
	"time"
)

func report[In any](t *testing.T, name string, fn func(*In), queries ...map[string]string) {
	t.Helper()
	r, err := Register(fn)
	if err != nil {
		t.Logf("%-34s REFUSED: %v", name, err)
		return
	}
	t.Logf("%-34s defaults=%v", name, r.Defaulted())
	for _, q := range queries {
		in, err := r.Decode(q, false)
		t.Logf("%-34s   q=%v -> %+v err=%v", "", q, in, err)
	}
}

// S1 public API, GitHub-style paging: non-zero default.
type ghList struct {
	Query struct {
		PerPage int
		Page    int
	}
}

// S2/S3 zero and non-zero bool defaults side by side.
type flags struct {
	Query struct {
		Archived bool
		Notify   bool
	}
}

// S5 metrics window: "last 24 hours" and "today".
type window struct{ Query struct{ From time.Time } }

// S6 sparse fieldsets: a slice default.
type fields struct{ Query struct{ Fields []string } }

// S7 a default written conditionally.
type sorted struct{ Query struct{ Sort string } }

// S8 create vs patch body.
type createRepo struct {
	Body struct {
		Name       string
		Visibility string
		AutoInit   bool
	}
}
type patchRepo struct {
	Body struct {
		Name       Opt[string]
		Visibility Opt[string]
	}
}

// S10 an Opt given a default.
type optDefault struct{ Query struct{ Limit Opt[int] } }

// S11 header default.
type lang struct {
	Header struct{ AcceptLanguage string }
}

// S12 one shared section, two routes, two defaults.
type Paging struct {
	Limit  int
	Offset int
}
type listA struct{ Query struct{ Paging } }
type listB struct {
	Query struct {
		Paging
		Search Opt[string]
	}
}

// S13 a default derived from another field.
type derived struct {
	Query struct {
		Start int
		End   int
	}
}

// S14 the default happens to equal the poison value (worst case of the trick).
type unlucky struct{ Query struct{ N int } }

func TestScenarios(t *testing.T) {
	report(t, "S1 github per_page=30", func(in *ghList) { in.Query.PerPage = 30; in.Query.Page = 1 },
		map[string]string{}, map[string]string{"perpage": "100"})
	report(t, "S2/S3 archived=false notify=true", func(in *flags) { in.Query.Archived = false; in.Query.Notify = true },
		map[string]string{})
	report(t, "S2' archived default by omission", func(in *flags) { in.Query.Notify = true },
		map[string]string{})
	report(t, "S5a from = now-24h", func(in *window) { in.Query.From = time.Now().Add(-24 * time.Hour) })
	report(t, "S5b from = start of today", func(in *window) { in.Query.From = time.Now().Truncate(24 * time.Hour) })
	report(t, "S7 sort set only when empty", func(in *sorted) {
		if in.Query.Sort == "" {
			in.Query.Sort = "created"
		}
	})
	report(t, "S8 create: visibility=private", func(in *createRepo) { in.Body.Visibility = "private"; in.Body.AutoInit = false },
		map[string]string{"name": "x"}, map[string]string{})
	report(t, "S10 Opt with a default", func(in *optDefault) { in.Query.Limit = Opt[int]{v: 50, ok: true} })
	report(t, "S11 Accept-Language=en", func(in *lang) { in.Header.AcceptLanguage = "en" }, map[string]string{})
	report(t, "S12a shared Paging, limit 20", func(in *listA) { in.Query.Limit = 20; in.Query.Offset = 0 }, map[string]string{})
	report(t, "S12b shared Paging, limit 50", func(in *listB) { in.Query.Limit = 50; in.Query.Offset = 0 }, map[string]string{})
	report(t, "S12c offset forgotten", func(in *listB) { in.Query.Limit = 50 }, map[string]string{})
	report(t, "S13 end = start + 10", func(in *derived) { in.Query.End = in.Query.Start + 10 })
	report(t, "S14 default == poison value", func(in *unlucky) { in.Query.N = -7_777_777 }, map[string]string{})
}

// S6: decoding into a shallow copy of the defaulted struct shares the slice.
func TestSliceDefaultAliasing(t *testing.T) {
	r, err := Register(func(in *fields) { in.Query.Fields = []string{"id", "name"} })
	if err != nil {
		t.Fatal(err)
	}
	for _, shallow := range []bool{true, false} {
		r2, _ := Register(func(in *fields) { in.Query.Fields = []string{"id", "name"} })
		a, _ := r2.Decode(map[string]string{}, shallow)
		a.Query.Fields[0] = "password_hash" // a handler appending/rewriting its own copy
		b, _ := r2.Decode(map[string]string{}, shallow)
		t.Logf("shallow=%v: next request's default Fields = %v", shallow, b.Query.Fields)
	}
	_ = r
}
