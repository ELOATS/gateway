package adapters

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-gateway/core/pkg/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- AnthropicProtocol 单元测试 ---

func TestAnthropicProtocol_AuthHeaders(t *testing.T) {
	p := &AnthropicProtocol{}
	h := p.AuthHeaders("sk-ant-test-key")

	assert.Equal(t, "sk-ant-test-key", h.Get("x-api-key"), "应使用 x-api-key 而非 Authorization")
	assert.Equal(t, anthropicAPIVersion, h.Get("anthropic-version"), "应携带 API 版本号")
	assert.Empty(t, h.Get("Authorization"), "Anthropic 不应设置 Authorization 头")
}

func TestOpenAIProtocol_AuthHeaders(t *testing.T) {
	p := &OpenAIProtocol{}
	h := p.AuthHeaders("sk-openai-test")

	assert.Equal(t, "Bearer sk-openai-test", h.Get("Authorization"))
	assert.Empty(t, h.Get("x-api-key"), "OpenAI 不应设置 x-api-key")
}

func TestAnthropicProtocol_EncodeRequest_Basic(t *testing.T) {
	p := &AnthropicProtocol{}
	req := &models.ChatCompletionRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []models.Message{
			{Role: "user", Content: "Hello Claude"},
		},
	}

	data, headers, err := p.EncodeRequest(req)
	require.NoError(t, err)

	assert.Equal(t, "application/json", headers.Get("Content-Type"))
	assert.Equal(t, "application/json", headers.Get("Accept"))

	var ar anthropicRequest
	err = json.Unmarshal(data, &ar)
	require.NoError(t, err)

	assert.Equal(t, "claude-sonnet-4-20250514", ar.Model)
	assert.Equal(t, defaultMaxTokens, ar.MaxTokens)
	assert.Empty(t, ar.System, "无 system 消息时 system 字段应为空")
	assert.Len(t, ar.Messages, 1)
	assert.Equal(t, "user", ar.Messages[0].Role)
}

func TestAnthropicProtocol_EncodeRequest_SystemExtraction(t *testing.T) {
	p := &AnthropicProtocol{}
	req := &models.ChatCompletionRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []models.Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello"},
		},
	}

	data, _, err := p.EncodeRequest(req)
	require.NoError(t, err)

	var ar anthropicRequest
	json.Unmarshal(data, &ar)

	assert.Equal(t, "You are a helpful assistant.", ar.System, "system 消息应被提取到顶层字段")
	assert.Len(t, ar.Messages, 1, "system 消息不应出现在 messages 数组中")
	assert.Equal(t, "user", ar.Messages[0].Role)
}

func TestAnthropicProtocol_EncodeRequest_StreamHeaders(t *testing.T) {
	p := &AnthropicProtocol{}
	req := &models.ChatCompletionRequest{
		Model:  "claude-sonnet-4-20250514",
		Stream: true,
		Messages: []models.Message{
			{Role: "user", Content: "Hi"},
		},
	}

	data, headers, err := p.EncodeRequest(req)
	require.NoError(t, err)
	assert.Equal(t, "text/event-stream", headers.Get("Accept"))

	var ar anthropicRequest
	json.Unmarshal(data, &ar)
	assert.True(t, ar.Stream)
}

func TestAnthropicProtocol_DecodeResponse_Success(t *testing.T) {
	p := &AnthropicProtocol{}
	body := strings.NewReader(`{
		"id": "msg_01XFDUDYJgAACzvnptvVoYEL",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello! How can I help you?"}],
		"model": "claude-sonnet-4-20250514",
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 25, "output_tokens": 10}
	}`)

	resp, err := p.DecodeResponse(body, http.StatusOK)
	require.NoError(t, err)

	assert.Equal(t, "msg_01XFDUDYJgAACzvnptvVoYEL", resp.ID)
	assert.Equal(t, "chat.completion", resp.Object)
	assert.Equal(t, "claude-sonnet-4-20250514", resp.Model)
	assert.Len(t, resp.Choices, 1)
	assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
	assert.Equal(t, "Hello! How can I help you?", resp.Choices[0].Message.GetText())
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
	assert.Equal(t, 25, resp.Usage.PromptTokens)
	assert.Equal(t, 10, resp.Usage.CompletionTokens)
	assert.Equal(t, 35, resp.Usage.TotalTokens)
}

func TestAnthropicProtocol_DecodeResponse_Error(t *testing.T) {
	p := &AnthropicProtocol{}
	body := strings.NewReader(`{
		"type": "error",
		"error": {"type": "invalid_request_error", "message": "max_tokens is required"}
	}`)

	_, err := p.DecodeResponse(body, http.StatusBadRequest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_tokens is required")
	assert.Contains(t, err.Error(), "invalid_request_error")
}

func TestAnthropicProtocol_DecodeResponse_StopReasonMapping(t *testing.T) {
	tests := []struct {
		stopReason     string
		expectedFinish string
	}{
		{"end_turn", "stop"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
		{"tool_use", "tool_calls"},
	}

	for _, tt := range tests {
		t.Run(tt.stopReason, func(t *testing.T) {
			p := &AnthropicProtocol{}
			body := strings.NewReader(fmt.Sprintf(`{
				"id": "msg_test",
				"type": "message",
				"role": "assistant",
				"content": [{"type": "text", "text": "ok"}],
				"model": "claude-sonnet-4-20250514",
				"stop_reason": "%s",
				"usage": {"input_tokens": 5, "output_tokens": 1}
			}`, tt.stopReason))

			resp, err := p.DecodeResponse(body, http.StatusOK)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedFinish, resp.Choices[0].FinishReason)
		})
	}
}

func TestAnthropicProtocol_DecodeStreamChunk(t *testing.T) {
	p := &AnthropicProtocol{}

	tests := []struct {
		name       string
		line       string
		expectResp bool
		expectDone bool
		expectText string
		expectRole string
	}{
		{
			name:       "empty line",
			line:       "",
			expectResp: false,
		},
		{
			name:       "event line (skipped)",
			line:       "event: content_block_delta",
			expectResp: false,
		},
		{
			name:       "message_start",
			line:       `data: {"type":"message_start","message":{"id":"msg_123","model":"claude-sonnet-4-20250514","role":"assistant"}}`,
			expectResp: true,
			expectRole: "assistant",
		},
		{
			name:       "content_block_delta",
			line:       `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
			expectResp: true,
			expectText: "Hello",
		},
		{
			name:       "message_delta with stop",
			line:       `data: {"type":"message_delta","delta":{"type":"end_turn"}}`,
			expectResp: true,
		},
		{
			name:       "message_stop",
			line:       `data: {"type":"message_stop"}`,
			expectDone: true,
		},
		{
			name:       "content_block_start (skipped)",
			line:       `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			expectResp: false,
		},
		{
			name:       "ping (skipped)",
			line:       `data: {"type":"ping"}`,
			expectResp: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, isDone, err := p.DecodeStreamChunk(tt.line)
			require.NoError(t, err)


			assert.Equal(t, tt.expectDone, isDone)

			if tt.expectResp {
				require.NotNil(t, resp, "expected non-nil response")
				if tt.expectText != "" {
					assert.Equal(t, tt.expectText, resp.Choices[0].Delta.Content)
				}
				if tt.expectRole != "" {
					assert.Equal(t, tt.expectRole, resp.Choices[0].Delta.Role)
				}
			} else if !tt.expectDone {
				assert.Nil(t, resp)
			}
		})
	}
}

// --- ProtocolAdapter 集成测试 ---

func TestProtocolAdapter_Anthropic_ChatCompletion(t *testing.T) {
	// 模拟 Anthropic API 服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证 Anthropic 特有的鉴权头
		assert.Equal(t, "sk-ant-test-key", r.Header.Get("x-api-key"))
		assert.Equal(t, anthropicAPIVersion, r.Header.Get("anthropic-version"))
		assert.Empty(t, r.Header.Get("Authorization"), "不应有 Bearer token")

		// 验证请求格式为 Anthropic 原生格式
		var body anthropicRequest
		json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, "claude-sonnet-4-20250514", body.Model)
		assert.Equal(t, "You are helpful.", body.System, "system 应被提取到顶层")
		assert.Len(t, body.Messages, 1)
		assert.Equal(t, "user", body.Messages[0].Role)
		assert.Equal(t, defaultMaxTokens, body.MaxTokens)

		// 返回 Anthropic 原生响应
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id": "msg_integration_test",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Hello from Claude!"}],
			"model": "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 15, "output_tokens": 5}
		}`)
	}))
	defer server.Close()

	// 使用 ProtocolAdapter + AnthropicProtocol（与 OpenAI 共用同一套传输逻辑）
	adapter := NewProtocolAdapter(&AnthropicProtocol{}, "sk-ant-test-key", server.URL, 5*time.Second)

	req := &models.ChatCompletionRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []models.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hi Claude"},
		},
	}

	resp, err := adapter.ChatCompletion(req)
	require.NoError(t, err)
	assert.Equal(t, "msg_integration_test", resp.ID)
	assert.Equal(t, "Hello from Claude!", resp.Choices[0].Message.GetText())
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
	assert.Equal(t, 20, resp.Usage.TotalTokens)
}

func TestProtocolAdapter_Anthropic_ChatCompletionStream(t *testing.T) {
	// 模拟 Anthropic 流式 API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "sk-ant-stream", r.Header.Get("x-api-key"))

		w.Header().Set("Content-Type", "text/event-stream")
		// Anthropic SSE 格式
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_stream","model":"claude-sonnet-4-20250514","role":"assistant"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" World"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_stop\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()

	adapter := NewProtocolAdapter(&AnthropicProtocol{}, "sk-ant-stream", server.URL, 5*time.Second)

	req := &models.ChatCompletionRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []models.Message{
			{Role: "user", Content: "Hi"},
		},
	}

	respCh, errCh := adapter.ChatCompletionStream(req)

	var result string
	for {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("流错误: %v", err)
			}
		case resp, ok := <-respCh:
			if !ok {
				goto DONE
			}
			if len(resp.Choices) > 0 && resp.Choices[0].Delta.Content != "" {
				result += resp.Choices[0].Delta.Content
			}
		case <-time.After(2 * time.Second):
			t.Fatal("流超时")
		}
	}

DONE:
	assert.Equal(t, "Hello World", result)
}

// 验证 NewProvider 工厂函数能正确创建 Anthropic 类型
func TestNewProvider_Anthropic(t *testing.T) {
	provider, err := NewProvider(Config{
		Type:    Anthropic,
		APIKey:  "sk-ant-test",
		URL:     "https://api.anthropic.com/v1/messages",
		Timeout: 30 * time.Second,
	})

	require.NoError(t, err)
	require.NotNil(t, provider)

	// 验证返回的是 ProtocolAdapter
	pa, ok := provider.(*ProtocolAdapter)
	assert.True(t, ok, "Anthropic provider 应返回 *ProtocolAdapter")
	assert.Equal(t, "sk-ant-test", pa.APIKey)

	// 验证内部 Protocol 是 AnthropicProtocol
	_, isAnthropic := pa.Protocol.(*AnthropicProtocol)
	assert.True(t, isAnthropic)
}

// 验证协议的对称性：OpenAI 和 Anthropic 走同一个 ProtocolAdapter，只是 Protocol 不同
func TestProtocolAdapter_ProviderAgnostic(t *testing.T) {
	openaiAdapter := NewProtocolAdapter(&OpenAIProtocol{}, "sk-openai", "http://openai.test", 5*time.Second)
	anthropicAdapter := NewProtocolAdapter(&AnthropicProtocol{}, "sk-ant", "http://anthropic.test", 5*time.Second)

	// 两者类型完全相同
	assert.IsType(t, openaiAdapter, anthropicAdapter)

	// 但 auth 头不同
	openaiAuth := openaiAdapter.Protocol.AuthHeaders("key")
	anthropicAuth := anthropicAdapter.Protocol.AuthHeaders("key")
	assert.NotEqual(t, openaiAuth.Get("Authorization"), anthropicAuth.Get("x-api-key"))
}
