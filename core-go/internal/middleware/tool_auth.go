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

// ToolAuthMiddleware validates access to agentic tool usage without forcing every
// chat request through the stricter tool-call body limit.
func ToolAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		prefix, rest, err := readBodyPrefix(c.Request.Body, toolAuthScanBytes)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "bad_request"})
			c.Abort()
			return
		}

		// Restore the original stream for downstream handlers by default.
		c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(prefix), rest))

		// Large non-tool requests, such as multimodal payloads with inline images,
		// should not be fully buffered here.
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

func readBodyPrefix(body io.ReadCloser, limit int64) ([]byte, io.Reader, error) {
	defer body.Close()

	prefix, err := io.ReadAll(io.LimitReader(body, limit))
	if err != nil {
		return nil, nil, err
	}

	return prefix, body, nil
}

func mightContainToolCall(prefix []byte) bool {
	return bytes.Contains(prefix, []byte(`"tools"`)) || bytes.Contains(prefix, []byte(`"tool_choice"`))
}
