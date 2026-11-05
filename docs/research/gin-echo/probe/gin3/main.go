package main

import (
	"fmt"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

func main() {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	var mu sync.Mutex
	var wg sync.WaitGroup
	var out []string
	r.GET("/p/:x", func(c *gin.Context) {
		own := c.Param("x")
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(200 * time.Millisecond)
			mu.Lock()
			out = append(out, fmt.Sprintf("request for %q: goroutine reads Param=%q URL.Path=%q", own, c.Param("x"), c.Request.URL.Path))
			mu.Unlock()
		}()
		c.String(200, "ok")
	})
	for _, p := range []string{"/p/first", "/p/second", "/p/third"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	wg.Wait()
	for _, l := range out { fmt.Println(l) }
}
