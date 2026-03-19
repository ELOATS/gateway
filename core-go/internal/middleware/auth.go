// Package middleware provides Gin middleware for security and cross-cutting concerns.
package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/gin-gonic/gin"
)

// AuthRequired 验证请求头中的 API Key 是否正确。
// 支持多 Key 匹配，并使用时序安全比较防止计时攻击。
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

		// 时序安全遍历比较
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

		// 注入 Key 信息，供后续中间件（如限流、配额管理）使用
		c.Set("key_label", matchedKey.Label)
		c.Set("api_key", matchedKey.Key)
		observability.AuthTotal.WithLabelValues("success", "").Inc()
		c.Next()
	}
}
