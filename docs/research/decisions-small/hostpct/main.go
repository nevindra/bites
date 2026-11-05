// Probe: what do h1.ParseHead and net/http do with a percent sign in a host?
package main

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"

	"github.com/nevindra/bites/internal/h1"
)

func main() {
	cases := []string{
		"GET http://a%41.example/ HTTP/1.1\r\nHost: x\r\n\r\n",
		"GET http://a%41.example/ HTTP/1.1\r\nHost: a%41.example\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: a%41.example\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: good.com%40evil.com\r\n\r\n",
		"GET http://good.com%40evil.com/ HTTP/1.1\r\nHost: x\r\n\r\n",
		"GET http://a%2fb/ HTTP/1.1\r\nHost: x\r\n\r\n",
		"GET http://a%zz/ HTTP/1.1\r\nHost: x\r\n\r\n",
		"GET http://%C3%BCber.example/ HTTP/1.1\r\nHost: x\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: %C3%BCber.example\r\n\r\n",
	}
	for _, c := range cases {
		var req h1.Request
		_, err := h1.ParseHead([]byte(c), &req, h1.DefaultLimits())
		ours := "accept host=" + fmt.Sprintf("%q", req.Host)
		if err != nil {
			ours = "refuse " + err.Error()
		}
		theirs := "?"
		r, e := http.ReadRequest(bufio.NewReader(strings.NewReader(c)))
		if e != nil {
			theirs = "refuse " + e.Error()
		} else {
			theirs = fmt.Sprintf("accept Host=%q URL.Host=%q", r.Host, r.URL.Host)
		}
		fmt.Printf("%q\n  h1:      %s\n  net/http: %s\n", strings.SplitN(c, "\r\n", 3)[0]+" | "+strings.SplitN(c, "\r\n", 3)[1], ours, theirs)
	}
}
