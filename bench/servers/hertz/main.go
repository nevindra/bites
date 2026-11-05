// The Hertz server of the harness (github.com/cloudwego/hertz, on its netpoll transport), with
// server.New and no middleware (server.Default would add Recovery), and its default JSON, sonic.
// Decoding is Hertz's own: c.Param, and c.BindQuery and c.BindJSON. The required checks are
// written by hand, the same in every framework server, so no validator library is in the path.
// A mistake is a plain 400, not problem+json.
package main

import (
	"context"
	"flag"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/nevindra/bites/bench/common"
)

type query struct {
	Page   int  `query:"page"`
	Expand bool `query:"expand"`
}

func main() {
	flag.Parse()
	common.StartStats()
	h := server.New(server.WithHostPorts(*common.Addr), server.WithDisablePrintRoute(true))
	h.POST("/users/:id", func(_ context.Context, c *app.RequestContext) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.String(consts.StatusBadRequest, "path.id: expected an integer")
			return
		}
		var q query
		if err := c.BindQuery(&q); err != nil || q.Page == 0 {
			c.String(consts.StatusBadRequest, "query.page: expected an integer")
			return
		}
		var body common.NewUser
		if err := c.BindJSON(&body); err != nil || !body.Complete() {
			c.String(consts.StatusBadRequest, "body: invalid")
			return
		}
		c.JSON(consts.StatusOK, common.Created{ID: id, Name: body.Name, Email: body.Email, Age: body.Age, Page: q.Page})
	})
	h.GET("/users/:id", func(_ context.Context, c *app.RequestContext) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.String(consts.StatusBadRequest, "path.id: expected an integer")
			return
		}
		c.JSON(consts.StatusOK, common.User{ID: id, Name: "ada", Email: "ada@example.dev", Age: 36})
	})
	h.GET("/block", func(_ context.Context, c *app.RequestContext) {
		time.Sleep(common.BlockDelay)
		c.JSON(consts.StatusOK, common.Blocked{OK: true})
	})
	h.Spin()
}
