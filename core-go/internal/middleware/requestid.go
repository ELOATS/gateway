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

// RequestID 中间件确保每个进入网关的请求都具备全局唯一的追踪标识（Trace ID）。
//
// 设计原则：
// 1. 透明传承：如果客户端或上游负载均衡（如 Nginx）已在 `X-Request-ID` 中提供了标识，网关将原文保留，实现跨架构透传。
// 2. 自动注入：对于首个接入网关且无 ID 的请求，自动生成 UUID4 标识，作为系统内部可观测性的根路径。
// 3. 闭环反馈：该 ID 会通过 Header 回写给调用方，极大地简化了用户报告异常时的排查定位成本。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader(HeaderXRequestID)
		if rid == "" {
			rid = uuid.New().String()
		}

		// 将 ID 注入 Gin 运行上下文，供业务 Handler、日志系统及下游微服务调用逻辑提取。
		c.Set(RequestIDKey, rid)

		// 将 ID 同步回写至响应头，确保客户端在收到响应时即可获得追踪凭据。
		c.Header(HeaderXRequestID, rid)

		c.Next()
	}
}
