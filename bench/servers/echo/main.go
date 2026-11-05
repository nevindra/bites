// The Echo server of the harness (github.com/labstack/echo/v5), with no middleware and its JSON
// serializer swapped for goccy/go-json, as Echo's documentation allows for speed. Decoding is
// Echo's own: c.Param, echo.BindQueryParams for the query and BindBody for the JSON body. The
// required checks are written by hand, the same in every framework server, so no validator
// library is in the path. Echo's default binder does not refuse unknown body members. A mistake
// is a plain 400, not problem+json.
package main

import (
	"flag"
	"net/http"
	"strconv"
	"time"

	gojson "github.com/goccy/go-json"
	"github.com/labstack/echo/v5"

	"github.com/nevindra/bites/bench/common"
)

type query struct {
	Page   int  `query:"page"`
	Expand bool `query:"expand"`
}

// fastJSON is echo.JSONSerializer over goccy/go-json.
type fastJSON struct{}

func (fastJSON) Serialize(c *echo.Context, i any, indent string) error {
	enc := gojson.NewEncoder(c.Response())
	if indent != "" {
		enc.SetIndent("", indent)
	}
	return enc.Encode(i)
}

func (fastJSON) Deserialize(c *echo.Context, i any) error {
	return gojson.NewDecoder(c.Request().Body).Decode(i)
}

func main() {
	flag.Parse()
	common.StartStats()
	e := echo.New()
	e.JSONSerializer = fastJSON{}
	e.POST("/users/:id", func(c *echo.Context) error {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			return c.String(http.StatusBadRequest, "path.id: expected an integer")
		}
		var q query
		if err := echo.BindQueryParams(c, &q); err != nil || q.Page == 0 {
			return c.String(http.StatusBadRequest, "query.page: expected an integer")
		}
		var body common.NewUser
		if err := echo.BindBody(c, &body); err != nil || !body.Complete() {
			return c.String(http.StatusBadRequest, "body: invalid")
		}
		return c.JSON(http.StatusOK, common.Created{ID: id, Name: body.Name, Email: body.Email, Age: body.Age, Page: q.Page})
	})
	e.GET("/users/:id", func(c *echo.Context) error {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			return c.String(http.StatusBadRequest, "path.id: expected an integer")
		}
		return c.JSON(http.StatusOK, common.User{ID: id, Name: "ada", Email: "ada@example.dev", Age: 36})
	})
	e.GET("/block", func(c *echo.Context) error {
		time.Sleep(common.BlockDelay)
		return c.JSON(http.StatusOK, common.Blocked{OK: true})
	})
	srv := &http.Server{Addr: *common.Addr, Handler: e}
	if err := srv.ListenAndServe(); err != nil {
		panic(err)
	}
}
