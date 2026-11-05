package engine

import "testing"

func TestStatusTextNamesTheStatusesTheFrameworkAnswersWith(t *testing.T) {
	for code, want := range map[int]string{200: "OK", 303: "See Other", 404: "Not Found", 405: "Method Not Allowed", 599: "Status 599"} {
		if got := StatusText(code); got != want {
			t.Errorf("StatusText(%d) = %q, want %q", code, got, want)
		}
	}
}
