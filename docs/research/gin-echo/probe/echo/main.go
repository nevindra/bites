package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v5"
)

type P struct {
	ID int `param:"id"`
}
type J struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
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
	fmt.Printf("CLIENT %s %s => %d X-Late=%q ct=%q body=%q\n", method, path, resp.StatusCode, resp.Header.Get("X-Late"), resp.Header.Get("Content-Type"), b)
}

func main() {
	e := echo.New()
	var mu sync.Mutex
	var wg sync.WaitGroup
	var out []string
	e.GET("/p/:x", func(c *echo.Context) error {
		own := c.Param("x")
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(200 * time.Millisecond)
			mu.Lock()
			out = append(out, fmt.Sprintf("request %q: goroutine reads Param=%q Path=%q", own, c.Param("x"), c.Request().URL.Path))
			mu.Unlock()
		}()
		return c.String(200, "ok")
	})
	e.GET("/u/:id", func(c *echo.Context) error {
		var p P
		if err := c.Bind(&p); err != nil {
			fmt.Printf("handler: bind err type=%T val=%v\n", err, err)
			return err
		}
		return c.JSON(200, p)
	})
	e.POST("/j", func(c *echo.Context) error {
		var j J
		if err := c.Bind(&j); err != nil {
			fmt.Printf("handler: bind err type=%T val=%v\n", err, err)
			return err
		}
		return c.JSON(200, j)
	})
	e.GET("/plain", func(c *echo.Context) error { return errors.New("db password is hunter2") })
	e.GET("/herr", func(c *echo.Context) error { return echo.NewHTTPError(418, "teapot") })
	e.GET("/twice", func(c *echo.Context) error {
		c.JSON(200, map[string]int{"a": 1})
		return c.JSON(201, map[string]int{"b": 2})
	})
	e.GET("/hdr", func(c *echo.Context) error {
		c.String(200, "body")
		c.Response().Header().Set("X-Late", "yes")
		return nil
	})
	e.GET("/errafter", func(c *echo.Context) error {
		c.String(200, "partial")
		return errors.New("late failure")
	})
	s := httptest.NewServer(e)
	defer s.Close()
	for _, p := range []string{"/p/first", "/p/second", "/p/third"} {
		get(s.URL, "GET", p, "")
	}
	wg.Wait()
	for _, l := range out { fmt.Println("LEAK", l) }
	get(s.URL, "GET", "/u/abc", "")
	get(s.URL, "POST", "/j", `{"name":`)
	get(s.URL, "POST", "/j", `{"age":"x"}`)
	get(s.URL, "GET", "/plain", "")
	get(s.URL, "GET", "/herr", "")
	get(s.URL, "GET", "/twice", "")
	get(s.URL, "GET", "/hdr", "")
	get(s.URL, "GET", "/errafter", "")
}
