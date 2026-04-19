// Package middleware 提供网关的通用安全与横切中间件。
package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/db"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/gin-gonic/gin"
)

// AuthRequired 构造一个强制鉴权中间件。
// 它支持双模验证：
// 1. 优先使用 TenantManager 从数据库动态查询租户与 Key 状态。
// 2. 如果数据库连接未就绪或未配置，则尝试匹配配置文件中的静态 fallbackKeys（主要用于本地开发与单元测试）。
func AuthRequired(tm db.TenantManager, fallbackKeys []config.APIKeyEntry) gin.HandlerFunc {
	return func(c *gin.Context) {
		rid, _ := c.Get(RequestIDKey)

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			observability.AuthTotal.WithLabelValues("failure", "missing_header").Inc()
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":      "unauthorized",
				"details":    "missing authorization header",
				"request_id": rid,
			})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			observability.AuthTotal.WithLabelValues("failure", "bad_format").Inc()
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":      "unauthorized",
				"details":    "format must be 'Bearer <key>'",
				"request_id": rid,
			})
			c.Abort()
			return
		}

		providedKey := parts[1]
		var matchedLabel string
		var matchedKeyStr string
		var tenantID uint

		if tm != nil {
			// 动态鉴权逻辑：从 DB/Cache 中检索租户绑定关系。
			tenant, ak, err := tm.GetTenantByKey(providedKey)
			if err == nil {
				matchedLabel = ak.Label
				matchedKeyStr = ak.Key
				tenantID = tenant.ID
				c.Set("api_key_id", ak.ID)
			}
		} else {
			// 降级鉴权逻辑（Fallback）：用于无数据库依赖的测试场景。
			// 使用 ConstantTimeCompare 来防御针对 API Key 的计时攻击（Timing Attack）。
			for _, entry := range fallbackKeys {
				if subtle.ConstantTimeCompare([]byte(providedKey), []byte(entry.Key)) == 1 {
					matchedLabel = entry.Label
					matchedKeyStr = entry.Key
					break
				}
			}
		}

		if matchedKeyStr == "" {
			observability.AuthTotal.WithLabelValues("failure", "invalid_key").Inc()
			c.JSON(http.StatusForbidden, gin.H{
				"error":      "forbidden",
				"details":    "invalid api key",
				"request_id": rid,
			})
			c.Abort()
			return
		}

		// 把租户信息注入上下文，供限流、配额和审计复用。
		c.Set("key_label", matchedLabel)
		c.Set("api_key", matchedKeyStr)
		if tenantID > 0 {
			c.Set("tenant_id", tenantID)
		}

		observability.AuthTotal.WithLabelValues("success", "").Inc()
		c.Next()
	}
}
