// Package middleware provides Gin middleware for security and cross-cutting concerns.
package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// AuthRequired 验证请求头中的 API Key 是否正确。
func AuthRequired(apiKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized", "details": "missing authorization header"})
			c.Abort()
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized", "details": "format must be 'Bearer <key>'"})
			c.Abort()
			return
		}

		if parts[1] != apiKey {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden", "details": "invalid api key"})
			c.Abort()
			return
		}

		c.Next()
	}
}
