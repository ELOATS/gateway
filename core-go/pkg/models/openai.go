// Package models 提供 AI 网关通用的数据结构模型。
package models

import (
	"strings"
)

// ChatCompletionRequest 表示网关标准的聊天补全请求模型（兼容 OpenAI V1 格式）。
//
// 它是网关的入参核心，定义了用户如何指定模型、消息流以及生成参数（如 Temperature）。
type ChatCompletionRequest struct {
	Model       string    `json:"model"`                 // 目标逻辑模型名称（网关会自动将其路由至真实的物理节点）
	Messages    []Message `json:"messages"`              // 对话历史列表，支持 system, user, assistant 等角色
	Temperature float64   `json:"temperature,omitempty"` // 采样温度：控制生成的随机性，范围通常在 0 到 2 之间
	TopP        float64   `json:"top_p,omitempty"`       // 核采样：另一种控制多样性的方式
	MaxTokens   int       `json:"max_tokens,omitempty"`  // 本次生成允许消耗的最大 Token 数量
	Stop        []string  `json:"stop,omitempty"`        // 停止词序列：当模型输出这些词时将自动停止生成
	Stream      bool      `json:"stream,omitempty"`      // 流式开关：启用后网关将返回 text/event-stream 格式的响应
	Tools       []Tool    `json:"tools,omitempty"`       // 工具定义：用于 Agent 模式，允许模型调用函数或访问外部能力
	ToolChoice  any       `json:"tool_choice,omitempty"` // 具体的工具选择策略（none, auto, 或指定 function）
}

// Tool 描述了网关支持的大语言模型工具增强能力（目前仅支持函数调用）。
type Tool struct {
	Type     string       `json:"type"`     // 工具类型，固定为 "function"
	Function FunctionCall `json:"function"` // 具体的函数定义
}

// FunctionCall 定义了一个符合 JSON Schema 规范的函数及其参数。
type FunctionCall struct {
	Name        string `json:"name"`                  // 函数名，必须唯一
	Description string `json:"description,omitempty"` // 函数功能描述，模型会根据此描述判断是否调用
	Parameters  any    `json:"parameters,omitempty"`  // 参数定义的 JSON Schema
}

// Message 描述了会话上下文中的单条消息单元。
type Message struct {
	Role      string     `json:"role"`                 // 参与者角色：system (系统指令), user (用户), assistant (助手), tool (工具返回)
	Content   any        `json:"content,omitempty"`    // 消息负载：支持 string 纯文本或 []ContentPart 多模态混合内容
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` // 如果角色是 assistant，此处可能包含模型生成的工具调用请求
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
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

// ChatCompletionResponse 表示请求执行成功后的完整同步响应结果。
type ChatCompletionResponse struct {
	ID      string   `json:"id"`      // 内部唯一请求 ID，用于链路追踪
	Object  string   `json:"object"`  // 对象类型标识，固定为 "chat.completion"
	Created int64    `json:"created"` // Unix 时间戳，表示响应生成的时刻
	Model   string   `json:"model"`   // 实际响应的底层物理模型名称
	Choices []Choice `json:"choices"` // 模型生成的候选方案列表（通常仅返回一个）
	Usage   Usage    `json:"usage"`   // 本次请求在供应商侧产生的精确 Token 消耗统计
}

// Choice 描述了单条补全候选结果。
type Choice struct {
	Index        int     `json:"index"`         // 候选选项的索引编号
	Message      Message `json:"message"`       // 最终生成的完整消息内容
	FinishReason string  `json:"finish_reason"` // 生成停止的原因：stop (正常停止), length (长度超限), tool_calls (发起工具请求)
}

// Usage 提供了本次调用的资源消耗原始数据，是计费与配额扣减的唯一权威来源。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`     // 经过供应商 tokenizer 计算后的输入 Token 数
	CompletionTokens int `json:"completion_tokens"` // 经过供应商 tokenizer 计算后的输出 Token 数
	TotalTokens      int `json:"total_tokens"`      // 总量 = 输入 + 输出
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
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}
