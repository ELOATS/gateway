package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	// RequestIDKey is the key used to store the Request-ID in the gin.Context.
	RequestIDKey = "request_id"
	// HeaderXRequestID is the standard header for request tracking.
	HeaderXRequestID = "X-Request-ID"
)

// RequestID 中间件确保每个请求都拥有唯一的标识 ID。
// 它会检查请求头中的 X-Request-ID，若不存在则生成一个新的 UUID。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader(HeaderXRequestID)
		if rid == "" {
			rid = uuid.New().String()
		}

		// 注入上下文供后续 Handler 使用
		c.Set(RequestIDKey, rid)

		// 设置响应头
		c.Header(HeaderXRequestID, rid)

		c.Next()
	}
}
