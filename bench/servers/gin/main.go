// The Gin server of the harness (github.com/gin-gonic/gin), configured as Gin's documentation
// says to for speed: release mode, gin.New() with no Logger or Recovery, and the build tag
// go_json so binding and rendering go through goccy/go-json (run.sh builds it with -tags go_json).
// Decoding is Gin's own: c.Param, ShouldBindQuery for the query and ShouldBindJSON for the body,
// with unknown body members refused (EnableDecoderDisallowUnknownFields). The required checks are
// written by hand, the same in every framework server, so no validator library is in the path.
// A mistake is a plain 400, not problem+json.
package main

import (
	"flag"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	"github.com/nevindra/bites/bench/common"
)

type query struct {
	Page   int  `form:"page"`
	Expand bool `form:"expand"`
}

func main() {
	flag.Parse()
	common.StartStats()
	gin.SetMode(gin.ReleaseMode)
	binding.EnableDecoderDisallowUnknownFields = true
	r := gin.New()
	r.POST("/users/:id", func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.String(http.StatusBadRequest, "path.id: expected an integer")
			return
		}
		var q query
		if err := c.ShouldBindQuery(&q); err != nil || q.Page == 0 {
			c.String(http.StatusBadRequest, "query.page: expected an integer")
			return
		}
		var body common.NewUser
		if err := c.ShouldBindJSON(&body); err != nil || !body.Complete() {
			c.String(http.StatusBadRequest, "body: invalid")
			return
		}
		c.JSON(http.StatusOK, common.Created{ID: id, Name: body.Name, Email: body.Email, Age: body.Age, Page: q.Page})
	})
	r.GET("/users/:id", func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.String(http.StatusBadRequest, "path.id: expected an integer")
			return
		}
		c.JSON(http.StatusOK, common.User{ID: id, Name: "ada", Email: "ada@example.dev", Age: 36})
	})
	r.GET("/block", func(c *gin.Context) {
		time.Sleep(common.BlockDelay)
		c.JSON(http.StatusOK, common.Blocked{OK: true})
	})
	srv := &http.Server{Addr: *common.Addr, Handler: r}
	if err := srv.ListenAndServe(); err != nil {
		panic(err)
	}
}
