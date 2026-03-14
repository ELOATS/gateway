// Package adapters 提供各种 AI 服务供应商的对接实现。
package adapters

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ai-gateway/core/pkg/models"
)

// Provider 定义了不同 AI 提供者的统一调用接口。
type Provider interface {
	ChatCompletion(req *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error)
}

// OpenAIAdapter 是用于调用 OpenAI 官方 API 的适配器。
type OpenAIAdapter struct {
	APIKey string
	Client *http.Client
}

// NewOpenAIAdapter 创建一个新的 OpenAI 适配器，并初始化复用的 HTTP 客户端。
func NewOpenAIAdapter(apiKey string) *OpenAIAdapter {
	return &OpenAIAdapter{
		APIKey: apiKey,
		Client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				IdleConnTimeout:     90 * time.Second,
				MaxIdleConnsPerHost: 100,
			},
		},
	}
}

// ChatCompletion 执行向 OpenAI API 的聊天补全请求。
func (a *OpenAIAdapter) ChatCompletion(req *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
	url := "https://api.openai.com/v1/chat/completions"
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", a.APIKey))

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai error (status %d): %s", resp.StatusCode, string(body))
	}

	var result models.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// MockAdapter 是用于开发测试的模拟适配器。
type MockAdapter struct {
	Name string
}

// ChatCompletion 模拟 AI 响应，用于本地开发与压测场景。
func (a *MockAdapter) ChatCompletion(req *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
	return &models.ChatCompletionResponse{
		ID:      fmt.Sprintf("mock-%s-%d", a.Name, time.Now().Unix()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []models.Choice{
			{
				Index: 0,
				Message: models.Message{
					Role:    "assistant",
					Content: fmt.Sprintf("这是来自网关节点 [%s] 的模拟响应。", a.Name),
				},
				FinishReason: "stop",
			},
		},
		Usage: models.Usage{
			PromptTokens:     10,
			CompletionTokens: 10,
			TotalTokens:      20,
		},
	}, nil
}
