// Package models 提供 AI 网关通用的数据结构模型。
package models

// ChatCompletionRequest 表示标准 OpenAI 聊天补全请求。
type ChatCompletionRequest struct {
	Model       string    `json:"model"`       // 使用的模型名称。
	Messages    []Message `json:"messages"`    // 聊天消息列表。
	Temperature float64   `json:"temperature,omitempty"` // 采样温度。
	Stream      bool      `json:"stream,omitempty"`      // 是否开启流式响应。
}

// Message 表示聊天对话中的单条消息。
type Message struct {
	Role    string `json:"role"`    // 角色（system, user, assistant）。
	Content string `json:"content"` // 消息内容。
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
