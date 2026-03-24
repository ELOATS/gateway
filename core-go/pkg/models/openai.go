// Package models 提供 AI 网关通用的数据结构模型。
package models

import (
	"strings"
)

// ChatCompletionRequest 表示标准 OpenAI 聊天补全请求。
type ChatCompletionRequest struct {
	Model       string    `json:"model"`                 // 使用的模型名称。
	Messages    []Message `json:"messages"`              // 聊天消息列表。
	Temperature float64   `json:"temperature,omitempty"` // 采样温度。
	Stream      bool      `json:"stream,omitempty"`      // 是否开启流式响应。
	Tools       []Tool    `json:"tools,omitempty"`       // Agent 工具列表
	ToolChoice  any       `json:"tool_choice,omitempty"` // 工具选择策略
}

// Tool 表示大语言模型可以调用的一个工具/函数。
type Tool struct {
	Type     string       `json:"type"` // 例如 "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall 描述 Function 工具的签名。
type FunctionCall struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// Message 表示聊天对话中的单条消息。
type Message struct {
	Role    string `json:"role"`              // 角色（system, user, assistant）。
	Content any    `json:"content,omitempty"` // 消息内容：支持字符串或 []ContentPart
}

// GetText 返回消息的纯文本部分，用于审计、分词等。
func (m *Message) GetText() string {
	if m.Content == nil {
		return ""
	}
	switch v := m.Content.(type) {
	case string:
		return v
	case []ContentPart:
		var sb strings.Builder
		for _, p := range v {
			if p.Type == "text" {
				sb.WriteString(p.Text)
			}
		}
		return sb.String()
	case []interface{}:
		var sb strings.Builder
		for _, p := range v {
			if m, ok := p.(map[string]interface{}); ok {
				if m["type"] == "text" {
					if text, ok := m["text"].(string); ok {
						sb.WriteString(text)
					}
				}
			}
		}
		return sb.String()
	}
	return ""
}

// ContentPart 类型表示多模态内容的一个片段。
type ContentPart struct {
	Type     string            `json:"type"`                // "text" 或 "image_url"
	Text     string            `json:"text,omitempty"`      // 当 type 为 text 时使用
	ImageURL *ContentPathImage `json:"image_url,omitempty"` // 当 type 为 image_url 时使用
}

// ContentPathImage 描述图片片段的 URL 详情。
type ContentPathImage struct {
	URL    string `json:"url"`              // 图片的 URL 或 base64
	Detail string `json:"detail,omitempty"` // 采样细节 (low, high, auto)
}

// ChatCompletionResponse 表示标准 OpenAI 聊天补全响应。
type ChatCompletionResponse struct {
	ID      string   `json:"id"`      // 请求唯一标识。
	Object  string   `json:"object"`  // 对象类型（如 chat.completion）。
	Created int64    `json:"created"` // 创建时间戳。
	Model   string   `json:"model"`   // 使用的模型名称。
	Choices []Choice `json:"choices"` // 补全选项列表。
	Usage   Usage    `json:"usage"`   // Token 消耗详情。
}

// Choice represents a single completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage provides token count metadata.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatCompletionStreamResponse 表示流式响应中的单个分块。
type ChatCompletionStreamResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
}

// StreamChoice 表示流式响应中的单个选项。
type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        ChoiceDelta `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

// ChoiceDelta 表示流式响应中增量的消息内容。
type ChoiceDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}
