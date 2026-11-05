// The Fiber server of the harness (github.com/gofiber/fiber/v3, on fasthttp), configured as
// Fiber's documentation says to for speed: no middleware, and its JSONEncoder and JSONDecoder
// set to goccy/go-json. One process (no prefork), as every other server. Decoding is Fiber's
// own: c.Params, c.Bind().Query and c.Bind().Body. The required checks are written by hand, the
// same in every framework server, so no validator library is in the path. Fiber's binder does
// not refuse unknown body members. A mistake is a plain 400, not problem+json.
package main

import (
	"flag"
	"strconv"
	"time"

	gojson "github.com/goccy/go-json"
	"github.com/gofiber/fiber/v3"

	"github.com/nevindra/bites/bench/common"
)

type query struct {
	Page   int  `query:"page"`
	Expand bool `query:"expand"`
}

func main() {
	flag.Parse()
	common.StartStats()
	app := fiber.New(fiber.Config{JSONEncoder: gojson.Marshal, JSONDecoder: gojson.Unmarshal})
	app.Post("/users/:id", func(c fiber.Ctx) error {
		id, err := strconv.ParseInt(c.Params("id"), 10, 64)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).SendString("path.id: expected an integer")
		}
		var q query
		if err := c.Bind().Query(&q); err != nil || q.Page == 0 {
			return c.Status(fiber.StatusBadRequest).SendString("query.page: expected an integer")
		}
		var body common.NewUser
		if err := c.Bind().Body(&body); err != nil || !body.Complete() {
			return c.Status(fiber.StatusBadRequest).SendString("body: invalid")
		}
		return c.JSON(common.Created{ID: id, Name: body.Name, Email: body.Email, Age: body.Age, Page: q.Page})
	})
	app.Get("/users/:id", func(c fiber.Ctx) error {
		id, err := strconv.ParseInt(c.Params("id"), 10, 64)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).SendString("path.id: expected an integer")
		}
		return c.JSON(common.User{ID: id, Name: "ada", Email: "ada@example.dev", Age: 36})
	})
	app.Get("/block", func(c fiber.Ctx) error {
		time.Sleep(common.BlockDelay)
		return c.JSON(common.Blocked{OK: true})
	})
	if err := app.Listen(*common.Addr, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
		panic(err)
	}
}
