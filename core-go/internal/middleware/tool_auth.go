package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
)

// ToolAuthMiddleware 拦截所有带有 'tools' 的大模型请求，验证该租户是否拥有代理/工具调用权限。
// 对于敏感且高风险的 Agent 架构，此中间件可被配置为白名单模式。
func ToolAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. 读取请求体
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
			c.Abort()
			return
		}

		// 2. 将 Request.Body 重置回去供下游 Handler 读取
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		// 3. 尝试解析为标准的 ChatCompletionRequest
		var req models.ChatCompletionRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			// 若非标准请求，放行交由后续业务线处理
			c.Next()
			return
		}

		// 4. 判断是否携带了 Tools 参数（是否正在唤起智能体能力）
		if len(req.Tools) > 0 || req.ToolChoice != nil {
			tier := c.GetString("key_label")
			
			// 策略示例：仅 "admin" 或 "premium" 租户允许使用外部工具。
			// 免费用户将被拦截，以节省工具调用的昂贵成本和安全暴露面。
			if tier != "admin" && tier != "premium" {
				c.JSON(http.StatusForbidden, gin.H{
					"error":   "tool_call_forbidden",
					"message": "The active API Key tier does not have permission to utilize Agentic tools or function calling.",
				})
				c.Abort()
				return
			}
			
			// 未来增强：针对 req.Tools 数组内的具体 Tool Name 进行更细粒度的白名单核对 (MCP Verification)。
		}

		c.Next()
	}
}
