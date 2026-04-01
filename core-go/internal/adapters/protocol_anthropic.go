package adapters

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ai-gateway/core/pkg/models"
)

// AnthropicProtocol 实现了网关标准请求与 Anthropic Claude API 原生格式的双向映射。
//
// Claude API 与 OpenAI 的核心差异：
//   - 鉴权使用 x-api-key 而非 Bearer token
//   - system prompt 不在 messages 数组中，而是顶层 "system" 字段
//   - max_tokens 为必填项
//   - 响应结构：content 为数组，stop_reason 替代 finish_reason
//   - 流式使用 event: content_block_delta 事件类型
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
	Type string `json:"type"`
	Text string `json:"text"`
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
		Type string `json:"type,omitempty"`
		Text string `json:"text,omitempty"`
	} `json:"delta,omitempty"`
	ContentBlock *anthropicContent `json:"content_block,omitempty"`
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
func (p *AnthropicProtocol) EncodeRequest(req *models.ChatCompletionRequest) ([]byte, http.Header, error) {
	ar := anthropicRequest{
		Model:     req.Model,
		MaxTokens: defaultMaxTokens,
		Stream:    req.Stream,
	}

	// Anthropic 要求 system 消息不在 messages 数组中，而是放在顶层字段。
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

	// 如果没有非 system 消息，补一个空 user 消息以满足 API 约束。
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
func (p *AnthropicProtocol) DecodeResponse(body io.Reader, statusCode int) (*models.ChatCompletionResponse, error) {
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
	for _, c := range ar.Content {
		if c.Type == "text" {
			textBuilder.WriteString(c.Text)
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
					Role:    "assistant",
					Content: textBuilder.String(),
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

// DecodeStreamChunk 将 Anthropic SSE 事件行解码为网关标准流式 chunk。
//
// Anthropic 的 SSE 事件类型：
//   - message_start: 携带消息元数据 (id, model)
//   - content_block_start: 新内容块开始
//   - content_block_delta: 增量文本内容（主要的文本产出事件）
//   - content_block_stop: 内容块结束
//   - message_delta: 消息级别的增量（stop_reason 等）
//   - message_stop: 流结束信号
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
