package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ai-gateway/core/pkg/models"
)

// AnthropicProtocol 实现了网关标准协议与 Anthropic Claude API 原生格式之间的双向映射转换。
//
// Claude API 与 OpenAI 的核心差异及映射策略：
//   - 鉴权机制：使用 HTTP Header `x-api-key` 而非标准的 `Bearer` token。
//   - 系统提示词：Claude 要求 System Prompt 必须独立于 Messages 数组，放置在顶层的 "system" 字段中。
//   - 字段约束：`max_tokens` 在 Claude API 中是强制必填项，而 OpenAI 为可选。
//   - 响应格式：文本内容包装在 `content` 数组中，且使用 `stop_reason` 字段替代 `finish_reason`。
//   - 流式传输：使用复杂的事件驱动模式（如 `content_block_delta`），需要状态机式的解析。
type AnthropicProtocol struct{}

const (
	anthropicAPIVersion   = "2023-06-01"
	anthropicDefaultModel = "claude-sonnet-4-20250514"
	defaultMaxTokens      = 4096
)

// AuthHeaders 返回 Anthropic 特有的鉴权头。
func (p *AnthropicProtocol) AuthHeaders(apiKey string) http.Header {
	h := make(http.Header)
	h.Set("x-api-key", apiKey)
	h.Set("anthropic-version", anthropicAPIVersion)
	return h
}

// anthropicRequest 是 Anthropic Claude API 的原生请求结构。
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Stream    bool               `json:"stream,omitempty"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"input_schema"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// anthropicResponse 是 Anthropic Claude API 的原生响应结构。
type anthropicResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Content    []anthropicContent `json:"content"`
	Model      string             `json:"model"`
	StopReason string             `json:"stop_reason"`
	Usage      anthropicUsage     `json:"usage"`
}

type anthropicContent struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// anthropicStreamEvent 是 Anthropic SSE 流中的一个事件数据块。
type anthropicStreamEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index,omitempty"`
	Delta *struct {
		Type string         `json:"type,omitempty"`
		Text string         `json:"text,omitempty"`
		PartialJson string `json:"partial_json,omitempty"`
	} `json:"delta,omitempty"`
	ContentBlock *anthropicContent  `json:"content_block,omitempty"`
	Message      *anthropicResponse `json:"message,omitempty"`
	Usage        *anthropicUsage    `json:"usage,omitempty"`
}

// anthropicErrorResponse 是 Anthropic API 的错误响应结构。
type anthropicErrorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// EncodeRequest 将网关标准请求转换为 Anthropic Claude 原生格式。
func (p *AnthropicProtocol) EncodeRequest(ctx context.Context, req *models.ChatCompletionRequest) ([]byte, http.Header, error) {
	ar := anthropicRequest{
		Model:     req.Model,
		MaxTokens: defaultMaxTokens,
		Stream:    req.Stream,
	}

	// 核心映射逻辑：分离 System Prompt。
	// Anthropic 强制要求对话历史中不能包含 'system' 角色的消息，必须提取到顶层字段。
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			ar.System = msg.GetText()
			continue
		}

		am := anthropicMessage{
			Role:    msg.Role,
			Content: convertContentForAnthropic(msg.Content),
		}
		ar.Messages = append(ar.Messages, am)
	}

	// 转换 Tool 定义
	if len(req.Tools) > 0 {
		for _, t := range req.Tools {
			if t.Type == "function" {
				ar.Tools = append(ar.Tools, anthropicTool{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					InputSchema: t.Function.Parameters,
				})
			}
		}
	}

	// 鲁棒性处理：如果请求中只有 system 消息导致 messages 为空，
	// 补入一条空 user 消息以规避 Anthropic API 的校验错误（400）。
	if len(ar.Messages) == 0 {
		ar.Messages = append(ar.Messages, anthropicMessage{
			Role:    "user",
			Content: "",
		})
	}

	data, err := json.Marshal(ar)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if req.Stream {
		headers.Set("Accept", "text/event-stream")
	} else {
		headers.Set("Accept", "application/json")
	}

	return data, headers, nil
}

// convertContentForAnthropic 将网关标准的 Content (string 或 []ContentPart) 转换为
// Anthropic 兼容的格式。Anthropic 支持多模态 content 数组。
func convertContentForAnthropic(content any) any {
	switch v := content.(type) {
	case string:
		return v
	case []models.ContentPart:
		var parts []map[string]any
		for _, part := range v {
			switch part.Type {
			case "text":
				parts = append(parts, map[string]any{
					"type": "text",
					"text": part.Text,
				})
			case "image_url":
				if part.ImageURL != nil {
					parts = append(parts, map[string]any{
						"type": "image",
						"source": map[string]any{
							"type": "url",
							"url":  part.ImageURL.URL,
						},
					})
				}
			}
		}
		if len(parts) == 0 {
			return ""
		}
		return parts
	default:
		return content
	}
}

// DecodeResponse 将 Anthropic 原生响应解码为网关标准格式。
func (p *AnthropicProtocol) DecodeResponse(ctx context.Context, body io.Reader, statusCode int) (*models.ChatCompletionResponse, error) {
	if statusCode != http.StatusOK {
		b, _ := io.ReadAll(body)
		// 尝试解析结构化错误
		var errResp anthropicErrorResponse
		if json.Unmarshal(b, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("anthropic error (status %d, type %s): %s",
				statusCode, errResp.Error.Type, errResp.Error.Message)
		}
		return nil, fmt.Errorf("anthropic error (status %d): %s", statusCode, string(b))
	}

	var ar anthropicResponse
	if err := json.NewDecoder(body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}

	// 将 Anthropic content 数组合并为单个文本响应。
	var textBuilder strings.Builder
	var toolCalls []models.ToolCall
	for _, c := range ar.Content {
		if c.Type == "text" {
			textBuilder.WriteString(c.Text)
		} else if c.Type == "tool_use" {
			args, _ := json.Marshal(c.Input)
			toolCalls = append(toolCalls, models.ToolCall{
				ID:   c.ID,
				Type: "function",
				Function: models.ToolFunction{
					Name:      c.Name,
					Arguments: string(args),
				},
			})
		}
	}

	finishReason := mapStopReason(ar.StopReason)

	return &models.ChatCompletionResponse{
		ID:      ar.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   ar.Model,
		Choices: []models.Choice{
			{
				Index: 0,
				Message: models.Message{
					Role:      "assistant",
					Content:   textBuilder.String(),
					ToolCalls: toolCalls,
				},
				FinishReason: finishReason,
			},
		},
		Usage: models.Usage{
			PromptTokens:     ar.Usage.InputTokens,
			CompletionTokens: ar.Usage.OutputTokens,
			TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens,
		},
	}, nil
}

// DecodeStreamChunk 解析 Anthropic 复杂的事件驱动 SSE 数据。
//
// 事件流状态机映射：
//   - message_start: 初始化事件，携带请求 ID 和模型 ID，用于建立上下文。
//   - content_block_start: 标志一个新内容块的开启（文本或工具调用）。
//   - content_block_delta: 投递核心增量数据。这是由于 Claude 支持多模态，数据可能在多个 Block 中并行产生。
//   - content_block_stop: 标志当前 Block 的结束。
//   - message_delta: 传递消息级别的元数据（如 Token 统计、停止原因）。
//   - message_stop: 触发 SSE 连接关闭。
func (p *AnthropicProtocol) DecodeStreamChunk(line string) (*models.ChatCompletionStreamResponse, bool, error) {
	line = strings.TrimSpace(line)

	// Anthropic SSE 使用 "event:" 前缀指定事件类型，"data:" 前缀传递 JSON。
	// 空行和 event: 行本身不含数据，跳过。
	if line == "" || strings.HasPrefix(line, "event:") {
		return nil, false, nil
	}

	if !strings.HasPrefix(line, "data: ") {
		return nil, false, nil
	}

	dataStr := strings.TrimPrefix(line, "data: ")

	var event anthropicStreamEvent
	if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
		return nil, false, fmt.Errorf("unmarshal anthropic stream chunk: %w", err)
	}

	switch event.Type {
	case "message_start":
		// message_start 携带消息 ID 和模型信息，映射为 role delta
		if event.Message != nil {
			return &models.ChatCompletionStreamResponse{
				ID:      event.Message.ID,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   event.Message.Model,
				Choices: []models.StreamChoice{
					{
						Index: 0,
						Delta: models.ChoiceDelta{Role: "assistant"},
					},
				},
			}, false, nil
		}
		return nil, false, nil

	case "content_block_delta":
		// 增量文本内容——这是流式输出的"主力"事件
		if event.Delta != nil && event.Delta.Text != "" {
			return &models.ChatCompletionStreamResponse{
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Choices: []models.StreamChoice{
					{
						Index: event.Index,
						Delta: models.ChoiceDelta{Content: event.Delta.Text},
					},
				},
			}, false, nil
		}
		return nil, false, nil

	case "message_delta":
		// message_delta 通常携带 stop_reason
		finishReason := ""
		if event.Delta != nil {
			finishReason = mapStopReason(event.Delta.Type)
		}
		if finishReason == "" {
			finishReason = "stop"
		}
		return &models.ChatCompletionStreamResponse{
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Choices: []models.StreamChoice{
				{
					Index:        0,
					FinishReason: finishReason,
				},
			},
		}, false, nil

	case "message_stop":
		// 流结束信号
		return nil, true, nil

	default:
		// content_block_start, content_block_stop, ping 等：跳过
		return nil, false, nil
	}
}

// mapStopReason 将 Anthropic 的 stop_reason 映射为 OpenAI 的 finish_reason。
func mapStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	default:
		if reason == "" {
			return "stop"
		}
		return reason
	}
}
