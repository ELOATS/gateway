// Package routes 负责协调 HTTP 路由端点。
package routes

import (
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/handlers"
	"github.com/ai-gateway/core/internal/middleware"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

// NewRouter 初始化并配置 API 网关的所有 HTTP 路由。
func NewRouter(h *handlers.ChatHandler, ah *handlers.AdminHandler, rdb *redis.Client, cfg *config.Config) *gin.Engine {
	r := gin.Default()
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// 健康检查端点：
	// /healthz 用于 Liveness 探针，仅表示进程存活。
	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "alive"}) })
	// /readyz 用于 Readiness 探针，表示服务已就绪可接收流量。
	r.GET("/readyz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ready"}) })
	// 兼容旧版端点
	r.GET("/health", func(c *gin.Context) { c.Redirect(301, "/healthz") })

	v1 := r.Group("/v1")
	v1.Use(middleware.RequestID())
	v1.Use(middleware.AuthRequired(cfg.APIKeys))
	v1.Use(middleware.RateLimiter(rdb, cfg.RateLimitQPS, cfg.RateLimitBurst))
	{
		v1.POST("/chat/completions", h.HandleChatCompletions)
	}

	// 管理接口：仅限 key_label 为 admin 的 API Key 访问。
	admin := r.Group("/admin")
	admin.Use(middleware.RequestID())
	admin.Use(middleware.AuthRequired(cfg.APIKeys))
	admin.Use(func(c *gin.Context) {
		if c.GetString("key_label") != "admin" {
			c.JSON(403, gin.H{"error": "forbidden", "message": "需要管理员权限"})
			c.Abort()
			return
		}
		c.Next()
	})
	{
		admin.GET("/nodes", ah.ListNodes)
		admin.GET("/strategies", ah.ListStrategies)
	}

	return r
}
