package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
)

type Item struct {
	ID   int    `uri:"id"`
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func build(imm bool, cap *[]string) *fiber.App {
	app := fiber.New(fiber.Config{Immutable: imm})
	app.Get("/p/:x", func(c fiber.Ctx) error {
		s := c.Params("x")
		q := c.Query("q")
		*cap = append(*cap, s, q)
		return c.SendString("ok")
	})
	app.Post("/b", func(c fiber.Ctx) error {
		b := c.Body()
		*cap = append(*cap, string(b[:0])) // placeholder
		bodyHold = b
		return c.SendString("ok")
	})
	app.Post("/item/:id", func(c fiber.Ctx) error {
		var it Item
		if err := c.Bind().URI(&it); err != nil {
			return err
		}
		if err := c.Bind().JSON(&it); err != nil {
			return err
		}
		return c.JSON(it)
	})
	app.Get("/n/:id", func(c fiber.Ctx) error {
		var it Item
		if err := c.Bind().URI(&it); err != nil {
			return err
		}
		return c.JSON(it)
	})
	app.Get("/twice", func(c fiber.Ctx) error {
		c.JSON(fiber.Map{"a": 1})
		return c.JSON(fiber.Map{"b": 2})
	})
	app.Get("/hdr", func(c fiber.Ctx) error {
		c.SendString("body")
		c.Set("X-Late", "1")
		c.Status(201)
		return nil
	})
	app.Get("/err", func(c fiber.Ctx) error { return errors.New("boom secret") })
	app.Get("/ferr", func(c fiber.Ctx) error { return fiber.NewError(418, "teapot") })
	app.Get("/ret", func(c fiber.Ctx) error { return nil })
	return app
}

var bodyHold []byte

func raw(addr string, reqs ...string) {
	c, _ := net.Dial("tcp", addr)
	defer c.Close()
	r := bufio.NewReader(c)
	for _, q := range reqs {
		c.Write([]byte(q))
		c.SetReadDeadline(time.Now().Add(time.Second))
		// read headers
		var cl int
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(strings.ToLower(l), "content-length:") {
				fmt.Sscanf(l[15:], "%d", &cl)
			}
			if l == "\r\n" {
				break
			}
		}
		io.CopyN(io.Discard, r, int64(cl))
	}
}

func get(app *fiber.App, method, path, body string) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req)
	if err != nil {
		fmt.Println(method, path, "ERR", err)
		return
	}
	b, _ := io.ReadAll(resp.Body)
	fmt.Printf("%s %s %q -> %d hdr=%v body=%q\n", method, path, body, resp.StatusCode, resp.Header, string(b))
}

func main() {
	for _, imm := range []bool{false, true} {
		var cap []string
		app := build(imm, &cap)
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		go app.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
		time.Sleep(200 * time.Millisecond)
		raw(ln.Addr().String(),
			"GET /p/AAAAAAAA?q=QQQQQQQQ HTTP/1.1\r\nHost: x\r\n\r\n",
			"GET /p/BBBBBBBB?q=ZZZZZZZZ HTTP/1.1\r\nHost: x\r\n\r\n")
		fmt.Printf("Immutable=%v captured after 2nd request on same conn: %q\n", imm, cap)
		bodyHold = nil
		raw(ln.Addr().String(),
			"POST /b HTTP/1.1\r\nHost: x\r\nContent-Length: 8\r\n\r\nAAAAAAAA",
			"POST /b HTTP/1.1\r\nHost: x\r\nContent-Length: 8\r\n\r\nBBBBBBBB")
		fmt.Printf("Immutable=%v body slice held from 1st POST, read after 2nd: %q\n", imm, bodyHold)
		app.Shutdown()
	}
	var cap []string
	app := build(false, &cap)
	get(app, "GET", "/n/abc", "")
	get(app, "GET", "/n/12", "")
	get(app, "POST", "/item/5", `{"name":"x","age":"notint"}`)
	get(app, "POST", "/item/5", `{"name":`)
	get(app, "POST", "/item/5", `{"name":"x","age":3}`)
	get(app, "GET", "/twice", "")
	get(app, "GET", "/hdr", "")
	get(app, "GET", "/err", "")
	get(app, "GET", "/ferr", "")
	get(app, "GET", "/ret", "")
	get(app, "GET", "/nope", "")
	get(app, "POST", "/ret", "")
}
