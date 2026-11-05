package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/valyala/fasthttp"
)

type In struct {
	ID   int    `uri:"id"`
	Name string `json:"name"`
}

func main() {
	app := fiber.New()
	app.Post("/a/:id", func(c fiber.Ctx) error {
		var in In
		if err := c.Bind().WithAutoHandling().URI(&in); err != nil {
			return err
		}
		if err := c.Bind().WithAutoHandling().JSON(&in); err != nil {
			return err
		}
		return c.JSON(in)
	})
	app.Get("/native", func(c fiber.Ctx) error { return c.SendString("hi") })
	app.Get("/std", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hi") }))
	for _, p := range [][3]string{{"POST", "/a/xx", `{"name":"a"}`}, {"POST", "/a/1", `{"name":`}} {
		req := httptest.NewRequest(p[0], p[1], strings.NewReader(p[2]))
		req.Header.Set("Content-Type", "application/json")
		resp, _ := app.Test(req)
		b, _ := io.ReadAll(resp.Body)
		fmt.Println(p[1], resp.StatusCode, string(b))
	}
	h := app.Handler()
	for _, path := range []string{"/native", "/std"} {
		var ctx fasthttp.RequestCtx
		ctx.Request.Header.SetMethod("GET")
		ctx.Request.SetRequestURI(path)
		ctx.Request.Header.SetHost("x")
		n := testing.AllocsPerRun(2000, func() {
			ctx.Response.Reset()
			h(&ctx)
		})
		fmt.Printf("%s allocs/op through app.Handler: %.1f\n", path, n)
	}
}
