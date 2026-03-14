// Package routes 负责协调 HTTP 路由端点。
package routes

import (
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/handlers"
	"github.com/ai-gateway/core/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewRouter 初始化并配置 API 网关的所有 HTTP 路由。
func NewRouter(h *handlers.ChatHandler, cfg *config.Config) *gin.Engine {
	r := gin.Default()
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.GET("/health", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })

	v1 := r.Group("/v1")
	v1.Use(middleware.RequestID())
	v1.Use(middleware.RateLimiter(cfg.RateLimitQPS, cfg.RateLimitBurst))
	v1.Use(middleware.AuthRequired(cfg.APIKeys))
	{
		v1.POST("/chat/completions", h.HandleChatCompletions)
	}
	return r
}
