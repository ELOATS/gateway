package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ai-gateway/core/pkg/models"
)

// GeminiProtocol 实现了 Google Gemini (包括 Vertex AI 与 Google AI 集成) 的协议映射。
type GeminiProtocol struct{}

func (p *GeminiProtocol) AuthHeaders(apiKey string) http.Header {
	h := make(http.Header)
	// 鉴权逻辑：Gemini 官方推荐将 API Key 放置在 `x-goog-api-key` 自定义头部，
	// 另一种常见的方案是附带在 URL 参数 `?key=...` 中。网关统一采用 Header 方式。
	h.Set("x-goog-api-key", apiKey)
	return h
}

// Gemini 请求结构
type geminiRequest struct {
	Contents         []geminiContent  `json:"contents"`
	GenerationConfig *geminiGenConfig `json:"generationConfig,omitempty"`
	Tools            []geminiTool     `json:"tools,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunction `json:"function_declarations"`
}

type geminiFunction struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text         string              `json:"text,omitempty"`
	FunctionCall *geminiFunctionCall `json:"functionCall,omitempty"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type geminiGenConfig struct {
	Temperature     float64  `json:"temperature,omitempty"`
	TopP            float64  `json:"topP,omitempty"`
	TopK            int      `json:"topK,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

// Gemini 响应结构
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
			Role  string       `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func (p *GeminiProtocol) EncodeRequest(ctx context.Context, req *models.ChatCompletionRequest) ([]byte, http.Header, error) {
	geminiReq := geminiRequest{
		Contents: make([]geminiContent, 0, len(req.Messages)),
	}

	// 将 OpenAI 风格的 Messages 映射为 Gemini 的 Contents 结构。
	for _, msg := range req.Messages {
		role := "user"
		if msg.Role == "assistant" {
			role = "model" // Gemini 协议中回复者的 Role 固定为 "model"
		}
		geminiReq.Contents = append(geminiReq.Contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: msg.GetText()}},
		})
	}

	geminiReq.GenerationConfig = &geminiGenConfig{
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		MaxOutputTokens: req.MaxTokens,
		StopSequences:   req.Stop,
	}

	// 转换 Tools
	if len(req.Tools) > 0 {
		var decls []geminiFunction
		for _, t := range req.Tools {
			if t.Type == "function" {
				decls = append(decls, geminiFunction{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					Parameters:  t.Function.Parameters,
				})
			}
		}
		if len(decls) > 0 {
			geminiReq.Tools = []geminiTool{{FunctionDeclarations: decls}}
		}
	}

	data, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, nil, err
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	return data, headers, nil
}

func (p *GeminiProtocol) DecodeResponse(ctx context.Context, body io.Reader, statusCode int) (*models.ChatCompletionResponse, error) {
	if statusCode != http.StatusOK {
		b, _ := io.ReadAll(body)
		return nil, fmt.Errorf("gemini error (status %d): %s", statusCode, string(b))
	}

	var geminiResp geminiResponse
	if err := json.NewDecoder(body).Decode(&geminiResp); err != nil {
		return nil, err
	}

	if len(geminiResp.Candidates) == 0 {
		return nil, fmt.Errorf("gemini returned no candidates")
	}

	cand := geminiResp.Candidates[0]
	var text strings.Builder
	var toolCalls []models.ToolCall
	for _, part := range cand.Content.Parts {
		if part.Text != "" {
			text.WriteString(part.Text)
		}
		if part.FunctionCall != nil {
			args, _ := json.Marshal(part.FunctionCall.Args)
			toolCalls = append(toolCalls, models.ToolCall{
				ID:   "gemini-call-" + part.FunctionCall.Name,
				Type: "function",
				Function: models.ToolFunction{
					Name:      part.FunctionCall.Name,
					Arguments: string(args),
				},
			})
		}
	}

	resp := &models.ChatCompletionResponse{
		ID:    "gemini-resp",
		Model: "gemini-pro", // 实际应从上下文获取
		Choices: []models.Choice{{
			Index: 0,
			Message: models.Message{
				Role:      "assistant",
				Content:   text.String(),
				ToolCalls: toolCalls,
			},
			FinishReason: strings.ToLower(cand.FinishReason),
		}},
		Usage: models.Usage{
			PromptTokens:     geminiResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      geminiResp.UsageMetadata.TotalTokenCount,
		},
	}

	return resp, nil
}

// DecodeStreamChunk 解析 Gemini 独特的 REST 流式数据。
//
// 协议差异警示：
// Gemini 的 streamGenerateContent 接口在某些环境下返回的并不是标准的 SSE (data: ...)，
// 而是一个被方括号包裹的 JSON 数组形式，或者是一个逐行生成的完整 JSON 对象流。
// 解码器必须具备剥离逗号前缀和方括号的能力，确保每一行都能被独立 Marshal。
func (p *GeminiProtocol) DecodeStreamChunk(line string) (*models.ChatCompletionStreamResponse, bool, error) {
	line = strings.TrimSpace(line)
	// 过滤 JSON 数组的分隔符 ([ , ]) 以及空行
	if line == "" || line == "[" || line == "]" || line == "," {
		return nil, false, nil
	}

	// 移除可能存在的逗号前缀（针对 JSON 数组项）
	line = strings.TrimPrefix(line, ",")

	var gr geminiResponse
	if err := json.Unmarshal([]byte(line), &gr); err != nil {
		return nil, false, nil // 无法解析的行忽略
	}

	if len(gr.Candidates) == 0 {
		return nil, false, nil
	}

	cand := gr.Candidates[0]
	var text strings.Builder
	for _, part := range cand.Content.Parts {
		text.WriteString(part.Text)
	}

	isDone := cand.FinishReason != "" && cand.FinishReason != "NONE"

	return &models.ChatCompletionStreamResponse{
		Choices: []models.StreamChoice{{
			Index: 0,
			Delta: models.ChoiceDelta{
				Content: text.String(),
			},
			FinishReason: strings.ToLower(cand.FinishReason),
		}},
	}, isDone, nil
}
