// Package middleware 提供网关的通用安全与横切中间件。
package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/gin-gonic/gin"
)

// AuthRequired 校验请求头中的 API Key 是否有效。
// 它支持多 Key 配置，并使用常量时间比较降低时序攻击风险。
func AuthRequired(keys []config.APIKeyEntry) gin.HandlerFunc {
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
		var matchedKey *config.APIKeyEntry

		// 使用常量时间比较遍历匹配，避免泄露哪一位开始不同。
		for _, entry := range keys {
			if subtle.ConstantTimeCompare([]byte(providedKey), []byte(entry.Key)) == 1 {
				matchedKey = &entry
				break
			}
		}

		if matchedKey == nil {
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
		c.Set("key_label", matchedKey.Label)
		c.Set("api_key", matchedKey.Key)
		observability.AuthTotal.WithLabelValues("success", "").Inc()
		c.Next()
	}
}
