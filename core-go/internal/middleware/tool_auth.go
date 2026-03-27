package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
)

const (
	toolAuthMaxBodyBytes = 20 * 1024 * 1024
	toolAuthScanBytes    = 256 * 1024
)

// ToolAuthMiddleware 检查当前请求是否尝试使用 tools / tool_choice。
// 它只做“工具调用权限”这一件事，并尽量避免把所有大请求都完整读入内存。
func ToolAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		prefix, rest, err := readBodyPrefix(c.Request.Body, toolAuthScanBytes)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
			c.Abort()
			return
		}

		// 默认先把读取过的前缀和剩余 body 重新拼回去，保证下游还能按原请求读取。
		c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(prefix), rest))

		// 对超大但明显不是工具调用的请求直接放行，避免误伤多模态场景。
		if c.Request.ContentLength > toolAuthMaxBodyBytes && !mightContainToolCall(prefix) {
			c.Next()
			return
		}

		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, toolAuthMaxBodyBytes)
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
			c.Abort()
			return
		}

		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		var req models.ChatCompletionRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			c.Next()
			return
		}

		if len(req.Tools) > 0 || req.ToolChoice != nil {
			tier := c.GetString("key_label")
			if tier != "admin" && tier != "premium" {
				c.JSON(http.StatusForbidden, gin.H{
					"error":   "tool_call_forbidden",
					"message": "The active API Key tier does not have permission to utilize Agentic tools or function calling.",
				})
				c.Abort()
				return
			}
		}

		c.Next()
	}
}

// readBodyPrefix 只读取有限前缀，用于做轻量探测而不是整包解析。
func readBodyPrefix(body io.ReadCloser, limit int64) ([]byte, io.Reader, error) {
	defer body.Close()

	prefix, err := io.ReadAll(io.LimitReader(body, limit))
	if err != nil {
		return nil, nil, err
	}

	return prefix, body, nil
}

// mightContainToolCall 基于 JSON 字段名做快速启发式判断。
func mightContainToolCall(prefix []byte) bool {
	return bytes.Contains(prefix, []byte(`"tools"`)) || bytes.Contains(prefix, []byte(`"tool_choice"`))
}
