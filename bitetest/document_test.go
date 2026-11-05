package bitetest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevindra/bites"
	"github.com/nevindra/bites/bitetest"
)

type docUser struct {
	ID   int64
	Name string
}

func docApp(extra func(*bites.App)) *bites.App {
	app := bites.New(bites.WithClock(bitetest.NewClock()))
	app.Get("/users/{id}", func(_ context.Context, in struct{ Path struct{ ID int64 } }) (docUser, error) {
		return docUser{ID: in.Path.ID, Name: "ada"}, nil
	})
	if extra != nil {
		extra(app)
	}
	app.OpenAPI("/openapi.json", bites.Info{Title: "Users", Version: "1"})
	return app
}

// fakeTB records a failure instead of ending the test, so a test can read the message.
type fakeTB struct {
	testing.TB
	msg    string
	failed bool
}

func (f *fakeTB) Helper()             {}
func (f *fakeTB) Name() string        { return "TestX" }
func (f *fakeTB) Logf(string, ...any) {}
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.failed, f.msg = true, fmt.Sprintf(format, args...)
	panic(f)
}

// run calls fn and reports whether it failed the test.
func (f *fakeTB) run(fn func(testing.TB)) {
	defer func() {
		if r := recover(); r != nil && r != f {
			panic(r)
		}
	}()
	fn(f)
}

func TestDocumentPassesWhenTheFileHoldsTheAppsDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "openapi.json")
	t.Setenv(bitetest.UpdateEnv, "1")
	bitetest.Document(t, docApp(nil), path)
	t.Setenv(bitetest.UpdateEnv, "")
	bitetest.Document(t, docApp(nil), path)
	b, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(b), "}\n") || strings.HasSuffix(string(b), "\n\n") || !strings.Contains(string(b), "\n  \"") {
		t.Fatalf("the file is not canonical: %q", b)
	}
}

func TestDocumentWritesKeysInSortedOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.json")
	t.Setenv(bitetest.UpdateEnv, "1")
	bitetest.Document(t, docApp(nil), path)
	b, _ := os.ReadFile(path)
	s := string(b)
	if !(strings.Index(s, `"components"`) < strings.Index(s, `"info"`) && strings.Index(s, `"info"`) < strings.Index(s, `"openapi"`) && strings.Index(s, `"openapi"`) < strings.Index(s, `"paths"`)) {
		t.Fatalf("top-level keys are not sorted:\n%s", s)
	}
}

func TestDocumentOfAMissingFileFailsWithTheInstructionAndWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none.json")
	f := &fakeTB{}
	f.run(func(tb testing.TB) { bitetest.Document(tb, docApp(nil), path) })
	if !f.failed || !strings.Contains(f.msg, "does not exist yet") || !strings.Contains(f.msg, "BITETEST_UPDATE=1 go test -run '^TestX$'") {
		t.Fatalf("got failed=%v %q", f.failed, f.msg)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the file was written without BITETEST_UPDATE")
	}
}

func TestDocumentNamesEachStructuralChangeAndTheWayToAcceptThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.json")
	t.Setenv(bitetest.UpdateEnv, "1")
	bitetest.Document(t, docApp(nil), path)
	t.Setenv(bitetest.UpdateEnv, "")
	changed := docApp(func(app *bites.App) {
		app.Get("/teams", func(context.Context, struct{}) (docUser, error) { return docUser{}, nil })
	})
	f := &fakeTB{}
	f.run(func(tb testing.TB) { bitetest.Document(tb, changed, path) })
	if !f.failed {
		t.Fatal("a new route passed")
	}
	for _, want := range []string{"differs from " + path, "+ /paths/~1teams: {", "BITETEST_UPDATE=1 go test"} {
		if !strings.Contains(f.msg, want) {
			t.Errorf("the message lacks %q:\n%s", want, f.msg)
		}
	}
	t.Log(f.msg)
}

func TestDocumentOfAFileInAnotherLayoutSaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.json")
	t.Setenv(bitetest.UpdateEnv, "1")
	bitetest.Document(t, docApp(nil), path)
	t.Setenv(bitetest.UpdateEnv, "")
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), "\n", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeTB{}
	f.run(func(tb testing.TB) { bitetest.Document(tb, docApp(nil), path) })
	if !f.failed || !strings.Contains(f.msg, "another layout") {
		t.Fatalf("got failed=%v %q", f.failed, f.msg)
	}
}

func TestDocumentOfAnAppWithoutOpenAPIFailsTheTest(t *testing.T) {
	f := &fakeTB{}
	f.run(func(tb testing.TB) { bitetest.Document(tb, bites.New(), filepath.Join(t.TempDir(), "d.json")) })
	if !f.failed || !strings.Contains(f.msg, "OpenAPI was never called") {
		t.Fatalf("got failed=%v %q", f.failed, f.msg)
	}
}

func TestDocumentCapsTheLinesAndCountsTheRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.json")
	if err := os.WriteFile(path, []byte("{\"paths\":{}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := docApp(func(app *bites.App) {
		for _, p := range []string{"/a", "/b", "/c", "/d", "/e", "/f", "/g", "/h", "/i", "/j", "/k", "/l", "/m", "/n", "/o", "/p", "/q", "/r", "/s", "/t", "/u", "/v", "/w", "/x", "/y", "/z", "/aa", "/bb", "/cc", "/dd", "/ee", "/ff", "/gg", "/hh", "/ii", "/jj", "/kk", "/ll", "/mm", "/nn", "/oo", "/pp"} {
			app.Get(p, func(context.Context, struct{}) (docUser, error) { return docUser{}, nil })
		}
	})
	f := &fakeTB{}
	f.run(func(tb testing.TB) { bitetest.Document(tb, app, path) })
	if !f.failed || !strings.Contains(f.msg, "... and ") || !strings.Contains(f.msg, " more") || strings.Count(f.msg, "\n\t+ ") != 40 {
		t.Fatalf("got failed=%v %q", f.failed, f.msg)
	}
}

func TestDiffDocumentsReportsAddedRemovedAndChangedInASortedOrder(t *testing.T) {
	before := `{"b":1,"a":{"x":"old","gone":true},"list":[1,2,3],"p/q":{"~":1}}`
	after := `{"a":{"x":"new","added":{"k":[1]}},"b":1,"list":[1,5],"p/q":{"~":2}}`
	got, err := bitetest.DiffDocuments([]byte(before), []byte(after))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`+ /a/added: {"k":[1]}`,
		`- /a/gone: true`,
		`~ /a/x: "old" -> "new"`,
		`~ /list/1: 2 -> 5`,
		`- /list/2: 3`,
		`~ /p~1q/~0: 1 -> 2`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestDiffDocumentsIgnoresLayoutAndKeyOrderAndTruncatesLongValues(t *testing.T) {
	same, err := bitetest.DiffDocuments([]byte(`{"a":1,"b":[true]}`), []byte("{\n \"b\": [true],\n \"a\": 1\n}"))
	if err != nil || len(same) != 0 {
		t.Fatalf("got %v %v", same, err)
	}
	long := strings.Repeat("é", 100)
	got, _ := bitetest.DiffDocuments([]byte(`{"k":"`+long+`"}`), []byte(`{"k":"short"}`))
	if len(got) != 1 || !strings.Contains(got[0], `... -> "short"`) {
		t.Fatalf("got %q", got)
	}
	if _, err := bitetest.DiffDocuments([]byte(`{`), []byte(`{}`)); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
}

func TestDiffDocumentsReportsAChangedTypeAtTheRoot(t *testing.T) {
	got, _ := bitetest.DiffDocuments([]byte(`{"a":1}`), []byte(`[1]`))
	if len(got) != 1 || !strings.HasPrefix(got[0], "~ /: ") {
		t.Fatalf("got %q", got)
	}
}
