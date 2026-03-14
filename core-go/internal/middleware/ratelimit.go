package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ai-gateway/core/internal/observability"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// RateLimiter 返回一个 Gin 中间件，用于对全球请求速率进行令牌桶限流。
func RateLimiter(qps float64, burst int) gin.HandlerFunc {
	limiter := rate.NewLimiter(rate.Limit(qps), burst)

	return func(c *gin.Context) {
		if !limiter.Allow() {
			rid, _ := c.Get(RequestIDKey)
			label := c.GetString("key_label")
			if label == "" {
				label = "anonymous"
			}

			observability.RateLimitedTotal.WithLabelValues(label).Inc()

			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":      "rate_limit_exceeded",
				"message":    "请求过于频繁，请稍后再试。",
				"request_id": rid,
			})

			// 告知客户端重试时机（近似值）
			c.Header("Retry-After", strconv.Itoa(int(time.Second/time.Duration(qps))))
			c.Abort()
			return
		}
		c.Next()
	}
}
