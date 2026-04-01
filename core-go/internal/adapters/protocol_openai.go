package adapters

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ai-gateway/core/pkg/models"
)

// OpenAIProtocol 实现了将网关标准请求与 OpenAI 兼容格式相互转换的逻辑。
type OpenAIProtocol struct{}

func (p *OpenAIProtocol) AuthHeaders(apiKey string) http.Header {
	h := make(http.Header)
	h.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	return h
}

func (p *OpenAIProtocol) EncodeRequest(req *models.ChatCompletionRequest) ([]byte, http.Header, error) {
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

func (p *OpenAIProtocol) DecodeResponse(body io.Reader, statusCode int) (*models.ChatCompletionResponse, error) {
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

	dataStr := strings.TrimPrefix(line, "data: ")
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
