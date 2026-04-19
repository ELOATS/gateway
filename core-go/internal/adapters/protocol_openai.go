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

// OpenAIProtocol 实现了官方 OpenAI Chat API 的数据互转逻辑。
// 由于它是网关的默认协议，许多兼容 OpenAI 格式的其他服务（如 DeepSeek, Qwen）也可以复用此类。
type OpenAIProtocol struct{}

func (p *OpenAIProtocol) AuthHeaders(apiKey string) http.Header {
	h := make(http.Header)
	h.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	return h
}

func (p *OpenAIProtocol) EncodeRequest(ctx context.Context, req *models.ChatCompletionRequest) ([]byte, http.Header, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
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

func (p *OpenAIProtocol) DecodeResponse(ctx context.Context, body io.Reader, statusCode int) (*models.ChatCompletionResponse, error) {
	if statusCode != http.StatusOK {
		b, _ := io.ReadAll(body)
		return nil, fmt.Errorf("provider error (status %d): %s", statusCode, string(b))
	}

	var result models.ChatCompletionResponse
	if err := json.NewDecoder(body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

func (p *OpenAIProtocol) DecodeStreamChunk(line string) (*models.ChatCompletionStreamResponse, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "data: ") {
		// 返回 nil, false, nil 表示跳过无效行
		return nil, false, nil
	}

	// OpenAI 使用 "data: " 协议头作为数据分界。
	dataStr := strings.TrimPrefix(line, "data: ")
	
	// 在 SSE 流中，[DONE] 表示服务端已完成所有内容推送，流即将关闭。
	if dataStr == "[DONE]" {
		return nil, true, nil
	}

	var streamResp models.ChatCompletionStreamResponse
	if err := json.Unmarshal([]byte(dataStr), &streamResp); err != nil {
		// 解析失败，作为一个非致命错误返回，供上层决定如何处理
		return nil, false, fmt.Errorf("unmarshal stream chunk: %w", err)
	}

	return &streamResp, false, nil
}
