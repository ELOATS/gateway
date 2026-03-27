package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	// RequestIDKey 是在 gin.Context 中存放请求 ID 的键名。
	RequestIDKey = "request_id"
	// HeaderXRequestID 是网关统一使用的请求追踪头。
	HeaderXRequestID = "X-Request-ID"
)

// RequestID 确保每个请求都拥有唯一的追踪标识。
// 如果上游已经传入 X-Request-ID，则沿用；否则由网关生成新的 UUID。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader(HeaderXRequestID)
		if rid == "" {
			rid = uuid.New().String()
		}

		// 注入上下文供后续 handler、日志和下游调用复用。
		c.Set(RequestIDKey, rid)

		// 同步写回响应头，便于客户端和日志系统关联。
		c.Header(HeaderXRequestID, rid)

		c.Next()
	}
}
