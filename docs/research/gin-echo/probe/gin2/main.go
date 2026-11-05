package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type J struct {
	Name string `json:"name" binding:"required"`
}

func get(base, method, path, body string) {
	req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("CLIENT ERR", err)
		return
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Printf("CLIENT %s %s => %d hdr(X-Late)=%q ct=%q body=%q\n", method, path, resp.StatusCode, resp.Header.Get("X-Late"), resp.Header.Get("Content-Type"), b)
}

func main() {
	gin.SetMode(gin.DebugMode)
	r := gin.New()
	r.Use(gin.Recovery())
	var mu sync.Mutex
	var seen []string
	var wg sync.WaitGroup
	r.GET("/p/:x", func(c *gin.Context) {
		x := c.Param("x")
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(300 * time.Millisecond)
			mu.Lock(); defer mu.Unlock()
			seen = append(seen, fmt.Sprintf("handler-for=%q goroutine-read Param=%q", x, c.Param("x")))
		}()
		c.String(200, "ok")
	})
	r.POST("/j", func(c *gin.Context) {
		var j J
		if err := c.Bind(&j); err != nil {
			c.JSON(500, gin.H{"after": "bind failed"})
			return
		}
	})
	r.GET("/twice", func(c *gin.Context) { c.JSON(200, gin.H{"a": 1}); c.JSON(201, gin.H{"b": 2}) })
	r.GET("/hdr", func(c *gin.Context) { c.String(200, "body"); c.Header("X-Late", "yes") })
	r.GET("/status", func(c *gin.Context) { c.String(200, "body"); c.Status(404) })
	s := httptest.NewServer(r)
	defer s.Close()
	get(s.URL, "GET", "/p/first", "")
	time.Sleep(50 * time.Millisecond)
	get(s.URL, "GET", "/p/second", "")
	wg.Wait()
	for _, l := range seen { fmt.Println("LEAK", l) }
	get(s.URL, "POST", "/j", `{"name":`)
	get(s.URL, "POST", "/j", `{}`)
	get(s.URL, "GET", "/twice", "")
	get(s.URL, "GET", "/hdr", "")
	get(s.URL, "GET", "/status", "")
}
