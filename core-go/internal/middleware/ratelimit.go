package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ai-gateway/core/internal/observability"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// RateLimiter returns a gin middleware that limits the request rate globally.
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
				"message":    "Too many requests, please try again later.",
				"request_id": rid,
			})

			// Inform client when they can retry (approximated)
			c.Header("Retry-After", strconv.Itoa(int(time.Second/time.Duration(qps))))
			c.Abort()
			return
		}
		c.Next()
	}
}
