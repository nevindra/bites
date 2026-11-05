package bitetest

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/nevindra/bites"
)

// UpdateEnv is the environment variable that makes Document write the file instead of
// comparing with it: set it to 1 (or true) for one run.
const UpdateEnv = "BITETEST_UPDATE"

const (
	maxDiffLines = 40
	maxValueText = 80
)

// Document holds the API document of app to the file at path, a JSON file checked into the
// repository. The document comes from App.OpenAPIDocument, never from a request, and is written
// in a canonical form (keys sorted, two-space indentation, one trailing newline) so the file
// and its diffs are stable. On a mismatch the test fails with one line per structural change
// (see DiffDocuments), not a byte diff. With BITETEST_UPDATE=1 in the environment the file is
// written (its directory is made) and the test passes; a file that does not exist yet fails
// the test with that instruction instead of being written, so a first run in CI cannot pass
// by recording whatever it found.
func Document(tb testing.TB, app *bites.App, path string) {
	tb.Helper()
	raw, err := app.OpenAPIDocument()
	if err != nil {
		tb.Fatalf("bitetest.Document: %v", err)
	}
	got, err := canonicalDocument(raw)
	if err != nil {
		tb.Fatalf("bitetest.Document: the API document is not valid JSON: %v", err)
	}
	if updating() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			tb.Fatalf("bitetest.Document: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			tb.Fatalf("bitetest.Document: %v", err)
		}
		tb.Logf("bitetest.Document: wrote %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		tb.Fatalf("bitetest.Document: %s does not exist yet: to record the current document, run\n\t%s", path, updateHint(tb))
	}
	if err != nil {
		tb.Fatalf("bitetest.Document: %v", err)
	}
	if bytes.Equal(got, want) {
		return
	}
	lines, err := DiffDocuments(want, got)
	if err != nil {
		tb.Fatalf("bitetest.Document: %s is not valid JSON: %v\n\tto replace it with the current document, run\n\t%s", path, err, updateHint(tb))
	}
	if len(lines) == 0 {
		tb.Fatalf("bitetest.Document: %s holds the same document in another layout (not the canonical form); to rewrite it, run\n\t%s", path, updateHint(tb))
	}
	shown := lines
	if len(shown) > maxDiffLines {
		shown = append(append([]string(nil), lines[:maxDiffLines]...), fmt.Sprintf("... and %d more", len(lines)-maxDiffLines))
	}
	noun := "changes"
	if len(lines) == 1 {
		noun = "change"
	}
	tb.Fatalf("bitetest.Document: the API document differs from %s (%d %s, - is in the file, + is in the app):\n\t%s\nto accept the app's document, run\n\t%s",
		path, len(lines), noun, strings.Join(shown, "\n\t"), updateHint(tb))
}

func updating() bool {
	switch strings.ToLower(os.Getenv(UpdateEnv)) {
	case "1", "true":
		return true
	}
	return false
}

func updateHint(tb testing.TB) string {
	return UpdateEnv + "=1 go test -run '^" + strings.ReplaceAll(tb.Name(), " ", "_") + "$' ."
}

// canonicalDocument is raw with keys sorted, indented by two spaces and ended by a newline.
func canonicalDocument(raw []byte) ([]byte, error) {
	v := jsontext.Value(bytes.Clone(raw))
	if err := v.Canonicalize(); err != nil {
		return nil, err
	}
	if err := v.Indent(jsontext.WithIndent("  ")); err != nil {
		return nil, err
	}
	return append(bytes.TrimRight(v, "\n"), '\n'), nil
}

// DiffDocuments compares two JSON documents structurally and returns one line per change, in a
// fixed order (object keys sorted, depth first, array elements by index), or none when they are
// the same document whatever their layout or key order. A line is a JSON Pointer (RFC 6901, so
// "/" in a path is written ~1) and the change: "- /p: v" is only in before, "+ /p: v" only in
// after, "~ /p: v -> w" in both with another value. A value longer than 80 bytes is cut with
// "...". An added or removed object or array is one line, not one per member, and an array is
// compared by index, so an element inserted at the front reads as changes all the way down.
// Numbers are compared as float64. It returns an error when either side is not JSON.
func DiffDocuments(before, after []byte) ([]string, error) {
	var a, b any
	if err := json.Unmarshal(before, &a); err != nil {
		return nil, fmt.Errorf("the first document: %w", err)
	}
	if err := json.Unmarshal(after, &b); err != nil {
		return nil, fmt.Errorf("the second document: %w", err)
	}
	var out []string
	diffValue(&out, "", a, b)
	return out, nil
}

func diffValue(out *[]string, path string, a, b any) {
	switch x := a.(type) {
	case map[string]any:
		if y, ok := b.(map[string]any); ok {
			keys := make([]string, 0, len(x)+len(y))
			for k := range x {
				keys = append(keys, k)
			}
			for k := range y {
				if _, ok := x[k]; !ok {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				p := path + "/" + pointerEscape(k)
				av, inA := x[k]
				bv, inB := y[k]
				switch {
				case !inB:
					*out = append(*out, "- "+p+": "+show(av))
				case !inA:
					*out = append(*out, "+ "+p+": "+show(bv))
				default:
					diffValue(out, p, av, bv)
				}
			}
			return
		}
	case []any:
		if y, ok := b.([]any); ok {
			for i := 0; i < len(x) || i < len(y); i++ {
				p := path + "/" + strconv.Itoa(i)
				switch {
				case i >= len(y):
					*out = append(*out, "- "+p+": "+show(x[i]))
				case i >= len(x):
					*out = append(*out, "+ "+p+": "+show(y[i]))
				default:
					diffValue(out, p, x[i], y[i])
				}
			}
			return
		}
	default:
		if reflect.DeepEqual(a, b) {
			return
		}
	}
	p := path
	if p == "" {
		p = "/"
	}
	*out = append(*out, "~ "+p+": "+show(a)+" -> "+show(b))
}

func pointerEscape(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}

// show is v as compact JSON with keys sorted, cut to maxValueText bytes on a rune boundary.
func show(v any) string {
	b, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		return fmt.Sprint(v)
	}
	s := string(b)
	if len(s) <= maxValueText {
		return s
	}
	n := maxValueText
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "..."
}
