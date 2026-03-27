package routes

import (
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/handlers"
	"github.com/ai-gateway/core/internal/middleware"
	"github.com/ai-gateway/core/internal/runtime"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

// NewRouter 组装所有 HTTP 路由。
// 这里刻意保持 chat 主路径很薄：只保留 request id、鉴权和 handler 驱动，
// 其余策略判断都已下沉到统一 pipeline 中。
func NewRouter(h *handlers.ChatHandler, ah *handlers.AdminHandler, rdb *redis.Client, cfg *config.Config, status *runtime.SystemStatus) *gin.Engine {
	r := gin.Default()
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.Static("/dashboard", "./dashboard")
	installStatusRoutes(r, status)

	v1 := r.Group("/v1")
	v1.Use(middleware.RequestID())
	v1.Use(middleware.AuthRequired(cfg.APIKeys))
	{
		v1.POST("/chat/completions", h.HandleChatCompletions)
	}

	admin := r.Group("/admin")
	admin.Use(middleware.RequestID())
	admin.Use(middleware.AuthRequired(cfg.APIKeys))
	admin.Use(func(c *gin.Context) {
		// 管理接口只允许 admin 级别 key 访问，避免运行时控制面泄露给普通租户。
		if c.GetString("key_label") != "admin" {
			c.JSON(403, gin.H{"error": "forbidden", "message": "需要管理员权限"})
			c.Abort()
			return
		}
		c.Next()
	})
	{
		admin.GET("/nodes", ah.ListNodes)
		admin.GET("/dependencies", ah.ListDependencies)
		admin.GET("/strategies", ah.ListStrategies)
		admin.POST("/nodes/:name/weight", ah.UpdateNodeWeight)
		admin.POST("/quota/reset", ah.ResetQuota)
	}

	return r
}
