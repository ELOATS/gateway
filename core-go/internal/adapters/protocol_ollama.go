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

// OllamaProtocol 实现了 Ollama 官方原生 API (/api/chat) 的映射转换逻辑。
// 注意：该协议主要用于本地私有化部署的模型推理。
type OllamaProtocol struct{}

func (p *OllamaProtocol) AuthHeaders(apiKey string) http.Header {
	// Ollama 默认为本地内网环境运行，通常不强制要求鉴权头部。
	// 若用户在前端通过 Nginx 等代理增加了鉴权，可通过配置 URL 带入，此处暂返空。
	return make(http.Header)
}

type ollamaRequest struct {
	Model    string           `json:"model"`
	Messages []ollamaMessage  `json:"messages"`
	Stream   bool             `json:"stream"`
	Options  *ollamaOptions   `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"top_p,omitempty"`
	TopK        int     `json:"top_k,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

type ollamaResponse struct {
	Model              string        `json:"model"`
	CreatedAt          string        `json:"created_at"`
	Message            ollamaMessage `json:"message"`
	Done               bool          `json:"done"`
	TotalDuration      int64         `json:"total_duration"`
	PromptEvalCount    int           `json:"prompt_eval_count"`
	EvalCount          int           `json:"eval_count"`
}

func (p *OllamaProtocol) EncodeRequest(ctx context.Context, req *models.ChatCompletionRequest) ([]byte, http.Header, error) {
	ollamaReq := ollamaRequest{
		Model:  req.Model,
		Stream: req.Stream,
		Options: &ollamaOptions{
			Temperature: req.Temperature,
			TopP:        req.TopP,
			NumPredict:  req.MaxTokens, // Ollama 使用 num_predict 来映射 OpenAI 的 max_tokens
		},
	}

	for _, msg := range req.Messages {
		ollamaReq.Messages = append(ollamaReq.Messages, ollamaMessage{
			Role:    msg.Role,
			Content: msg.GetText(),
		})
	}

	data, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, nil, err
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	return data, headers, nil
}

func (p *OllamaProtocol) DecodeResponse(ctx context.Context, body io.Reader, statusCode int) (*models.ChatCompletionResponse, error) {
	if statusCode != http.StatusOK {
		b, _ := io.ReadAll(body)
		return nil, fmt.Errorf("ollama error (status %d): %s", statusCode, string(b))
	}

	var or ollamaResponse
	if err := json.NewDecoder(body).Decode(&or); err != nil {
		return nil, err
	}

	return &models.ChatCompletionResponse{
		ID:    "ollama-resp-" + or.CreatedAt,
		Model: or.Model,
		Choices: []models.Choice{{
			Index: 0,
			Message: models.Message{
				Role:    or.Message.Role,
				Content: or.Message.Content,
			},
			FinishReason: "stop",
		}},
		Usage: models.Usage{
			PromptTokens:     or.PromptEvalCount,
			CompletionTokens: or.EvalCount,
			TotalTokens:      or.PromptEvalCount + or.EvalCount,
		},
	}, nil
}

func (p *OllamaProtocol) DecodeStreamChunk(line string) (*models.ChatCompletionStreamResponse, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, false, nil
	}

	var or ollamaResponse
	if err := json.Unmarshal([]byte(line), &or); err != nil {
		return nil, false, fmt.Errorf("unmarshal ollama chunk: %w", err)
	}

	return &models.ChatCompletionStreamResponse{
		Choices: []models.StreamChoice{{
			Index: 0,
			Delta: models.ChoiceDelta{
				Content: or.Message.Content,
			},
			FinishReason: func() string {
				if or.Done {
					return "stop"
				}
				return ""
			}(),
		}},
	}, or.Done, nil
}
