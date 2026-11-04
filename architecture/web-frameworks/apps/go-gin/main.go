// Полный: Gin — роутер, middleware, привязка и валидация JSON по тегам binding.
package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type item struct {
	Name string `json:"name" binding:"required"`
	Qty  int    `json:"qty"`
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.GET("/items/:id", func(c *gin.Context) {
		var p struct {
			ID int `uri:"id" binding:"required"`
		}
		if err := c.ShouldBindUri(&p); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": p.ID, "name": "item"})
	})
	r.POST("/items", func(c *gin.Context) {
		var it item
		if err := c.ShouldBindJSON(&it); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, it)
	})
	r.GET("/boom", func(c *gin.Context) { panic("boom: секрет в сообщении marker-7f3a9c") })
	r.Run(":8080")
}
