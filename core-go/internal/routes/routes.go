package routes

import (
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/db"
	"github.com/ai-gateway/core/internal/handlers"
	"github.com/ai-gateway/core/internal/middleware"
	"github.com/ai-gateway/core/internal/runtime"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

// NewRouter 构造并组装网关的所有 HTTP 路由映射。
//
// 设计原则：
// 1. 核心业务路径（/chat/completions）保持精简，仅串联 RequestID 与 Auth 鉴权中间件。
// 2. 具体的限流、配额、黑名单等业务策略已下沉到 Pipeline 层，不在此处堆叠中间件，以提高可读性和维护性。
// 3. 实现了管理面（Admin）与数据面（V1）的严格权限隔离。
func NewRouter(h *handlers.ChatHandler, ah *handlers.AdminHandler, th *handlers.TenantAdminHandler, bh *handlers.BillingHandler, rh *handlers.RerankHandler, tm db.TenantManager, rdb *redis.Client, cfg *config.Config, status *runtime.SystemStatus) *gin.Engine {
	r := gin.Default()
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.Static("/dashboard", "./dashboard")
	installStatusRoutes(r, status)

	v1 := r.Group("/v1")
	v1.Use(middleware.RequestID())
	v1.Use(middleware.AuthRequired(tm, cfg.APIKeys))
	{
		v1.POST("/chat/completions", h.HandleChatCompletions)
		if rh != nil {
			v1.POST("/rerank", rh.HandleRerank)
		}
	}

	admin := r.Group("/admin")
	admin.Use(middleware.RequestID())
	admin.Use(middleware.AuthRequired(tm, cfg.APIKeys))
	admin.Use(func(c *gin.Context) {
		// 安全策略：管理接口（控制面）仅允许拥有 "admin" 标签的 API Key 访问。
		// 这样可以确保即便是合法租户，也无法调用内部监控、权重修改或租户开通接口。
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

		if th != nil {
			admin.GET("/tenants", th.ListTenants)
			admin.POST("/tenants", th.CreateTenant)
			admin.POST("/tenants/:id/keys", th.CreateAPIKey)
			admin.GET("/prices", th.ListModelPrices)
			admin.POST("/prices", th.UpdateModelPrice)
		}

		if bh != nil {
			billingGrp := admin.Group("/billing")
			{
				billingGrp.GET("/tenants", bh.GetAllTenantsUsage)
				billingGrp.GET("/tenants/:id", bh.GetTenantSummary)
				billingGrp.GET("/reports/models", bh.GetModelUsageReport)
			}
		}
	}

	return r
}
