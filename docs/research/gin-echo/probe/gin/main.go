package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type P struct {
	ID int `uri:"id" binding:"required"`
}
type J struct {
	Name string `json:"name" binding:"required"`
	Age  int    `json:"age"`
}

func do(r http.Handler, method, path, body string) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	r.ServeHTTP(w, req)
	fmt.Printf("--> %s %s => %d hdr=%v body=%q\n", method, path, w.Code, w.Header(), w.Body.String())
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	var leaked *gin.Context
	done := make(chan string, 1)
	r.GET("/p/:x", func(c *gin.Context) {
		x := c.Param("x")
		leaked = c
		go func() {
			time.Sleep(200 * time.Millisecond)
			done <- fmt.Sprintf("goroutine saw Param(x)=%q (handler param was %q) path=%q", c.Param("x"), x, c.Request.URL.Path)
		}()
		c.String(200, "ok")
	})
	r.GET("/uri/:id", func(c *gin.Context) {
		var p P
		if err := c.ShouldBindUri(&p); err != nil {
			c.JSON(400, gin.H{"err": err.Error()})
			return
		}
		c.JSON(200, p)
	})
	r.GET("/uribind/:id", func(c *gin.Context) {
		var p P
		if err := c.BindUri(&p); err != nil {
			c.JSON(500, gin.H{"after": "bind failed"})
			fmt.Println("handler: BindUri err:", err, "errors:", c.Errors)
			return
		}
	})
	r.POST("/j", func(c *gin.Context) {
		var j J
		if err := c.Bind(&j); err != nil {
			fmt.Println("handler: Bind err:", err, "| c.Errors:", c.Errors.String())
			c.JSON(500, gin.H{"after": "bind failed"})
			return
		}
		c.JSON(200, j)
	})
	r.POST("/sj", func(c *gin.Context) {
		var j J
		if err := c.ShouldBindJSON(&j); err != nil {
			c.JSON(400, gin.H{"err": err.Error()})
			return
		}
		c.JSON(200, j)
	})
	r.GET("/twice", func(c *gin.Context) {
		c.JSON(200, gin.H{"a": 1})
		c.JSON(201, gin.H{"b": 2})
	})
	r.GET("/hdr", func(c *gin.Context) {
		c.String(200, "body")
		c.Header("X-Late", "yes")
		c.Status(404)
	})
	r.GET("/abort", func(c *gin.Context) {
		c.Abort()
		fmt.Println("code after Abort() ran; IsAborted=", c.IsAborted())
		c.String(200, "after abort")
	}, func(c *gin.Context) { fmt.Println("SECOND handler ran (should not)") })
	r.GET("/abortstatus", func(c *gin.Context) {
		c.AbortWithStatus(401)
		fmt.Println("after AbortWithStatus ran")
		c.String(200, "x")
	}, func(c *gin.Context) { fmt.Println("SECOND handler ran (should not)") })
	r.GET("/err", func(c *gin.Context) {
		c.Error(fmt.Errorf("boom"))
		c.String(200, "fine")
	})
	// real-server for goroutine leak test
	do(r, "GET", "/p/first", "")
	do(r, "GET", "/p/second", "")
	fmt.Println(<-done)
	fmt.Println("leaked==current ctx pointer reuse test, leaked.Param now =", leaked.Param("x"))
	do(r, "GET", "/uri/abc", "")
	do(r, "GET", "/uribind/abc", "")
	do(r, "POST", "/j", `{"name":`)
	do(r, "POST", "/j", `{"age":"x"}`)
	do(r, "POST", "/sj", `{"name":`)
	do(r, "POST", "/sj", `{"name":"a","age":"x"}`)
	do(r, "POST", "/sj", `{"age":1}`)
	do(r, "GET", "/twice", "")
	do(r, "GET", "/hdr", "")
	do(r, "GET", "/abort", "")
	do(r, "GET", "/abortstatus", "")
	do(r, "GET", "/err", "")
	fmt.Println("mode:", gin.Mode())
}
