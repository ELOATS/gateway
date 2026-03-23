package adapters

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-gateway/core/pkg/models"
	"github.com/stretchr/testify/assert"
)

func TestDynamicAdapter_ChatCompletion(t *testing.T) {
	// 1. 模拟一个支持多模态的 Provider
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证 Header 模板是否正确渲染
		assert.Equal(t, "Bearer sk-test-key", r.Header.Get("Authorization"))
		assert.Equal(t, "2023-01-01", r.Header.Get("X-API-Version"))

		// 验证 Body 映射（含 body_extra）
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, float64(100), body["max_tokens"]) // body_extra 注入

		// 验证多模态结构是否透传
		messages := body["messages"].([]any)
		lastMsg := messages[len(messages)-1].(map[string]any)
		content := lastMsg["content"].([]any)
		assert.Equal(t, "image_url", content[1].(map[string]any)["type"])

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chat-123","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"I see the image!"}}]}`))
	}))
	defer server.Close()

	// 2. 构造插件配置
	plugin := PluginConfig{
		Name:    "test-multimodal",
		BaseURL: server.URL,
		DefaultHeaders: map[string]string{
			"X-API-Version": "2023-01-01",
		},
	}
	plugin.RequestMapping.HeaderTemplate = map[string]string{
		"Authorization": "Bearer {{.Key}}",
	}
	plugin.RequestMapping.BodyExtra = map[string]any{
		"max_tokens": 100,
	}

	adapter := NewDynamicAdapter(plugin, "sk-test-key", 2*time.Second)

	// 3. 发起多模态模拟请求
	req := &models.ChatCompletionRequest{
		Model: "gpt-4-vision",
		Messages: []models.Message{
			{
				Role: "user",
				Content: []models.ContentPart{
					{Type: "text", Text: "What is this?"},
					{Type: "image_url", ImageURL: &models.ContentPathImage{URL: "https://example.com/a.png"}},
				},
			},
		},
	}

	resp, err := adapter.ChatCompletion(req)
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, "I see the image!", resp.Choices[0].Message.GetText())
}
