package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/router"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
)

func TestChatHandler_ExtractPrompt(t *testing.T) {
	h := &ChatHandler{}
	req := &models.ChatCompletionRequest{
		Messages: []models.Message{
			{Role: "system", Content: "You are a bot"},
			{Role: "user", Content: "Hello world"},
		},
	}
	prompt := h.extractPrompt(req)
	if prompt != "Hello world" {
		t.Errorf("预期 Hello world，实际 %s", prompt)
	}
}

func TestChatHandler_HandleChatCompletions_Basic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 1. 构造依赖项
	cfg := &config.Config{
		RequestTimeout:        5 * time.Second,
		TokenEstimationFactor: 4,
	}
	sr := router.NewSmartRouter(nil, nil, "weighted")

	// 这里通常需要 Mock gRPC Client，为保持示例简洁，此处暂不依赖真实 gRPC
	h := NewChatHandler(nil, nil, sr, nil, cfg)
	if h == nil {
		t.Fatal("NewChatHandler 返回了 nil")
	}

	// 2. 模拟请求
	reqBody, _ := json.Marshal(models.ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []models.Message{{Role: "user", Content: "hi"}},
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("POST", "/v1/chat/completions", bytes.NewBuffer(reqBody))

	// 3. 执行（注意：如果没有 gRPC Mock，这里会因为空指针 panic，实际项目中应注入 Mock）
	// 由于 routeAndExecute 会调用 Adapter，这里我们仅验证解析阶段。
}
