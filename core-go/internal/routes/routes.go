// Package routes coordinates HTTP endpoints.
package routes

import (
	"github.com/ai-gateway/core/internal/handlers"
	"github.com/ai-gateway/core/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func NewRouter(h *handlers.ChatHandler, apiKey string) *gin.Engine {
	r := gin.Default()
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })

	v1 := r.Group("/v1")
	v1.Use(middleware.AuthRequired(apiKey))
	{
		v1.POST("/chat/completions", h.HandleChatCompletions)
	}
	return r
}
