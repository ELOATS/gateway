// Package adapters 提供各种 AI 服务供应商的对接实现。
package adapters

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ai-gateway/core/pkg/models"
)

// Provider 定义了不同 AI 提供者的统一调用接口。
type Provider interface {
	ChatCompletion(req *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error)
	ChatCompletionStream(req *models.ChatCompletionRequest) (<-chan *models.ChatCompletionStreamResponse, <-chan error)
}

// OpenAIAdapter 是用于调用 OpenAI 官方 API 的适配器。
type OpenAIAdapter struct {
	APIKey  string
	URL     string
	Timeout time.Duration
	Client  *http.Client
}

// NewOpenAIAdapter 创建一个新的 OpenAI 适配器，配置由外部传入。
func NewOpenAIAdapter(apiKey, url string, timeout time.Duration) *OpenAIAdapter {
	return &OpenAIAdapter{
		APIKey:  apiKey,
		URL:     url,
		Timeout: timeout,
		Client: &http.Client{
			Timeout: timeout,
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
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", a.URL, bytes.NewBuffer(data))
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
		return nil, fmt.Errorf("provider error (status %d): %s", resp.StatusCode, string(body))
	}

	var result models.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

// ChatCompletionStream 执行流式补全请求。
func (a *OpenAIAdapter) ChatCompletionStream(req *models.ChatCompletionRequest) (<-chan *models.ChatCompletionStreamResponse, <-chan error) {
	respCh := make(chan *models.ChatCompletionStreamResponse)
	errCh := make(chan error, 1)

	go func() {
		defer close(respCh)
		defer close(errCh)

		req.Stream = true
		data, _ := json.Marshal(req)
		httpReq, _ := http.NewRequest("POST", a.URL, bytes.NewBuffer(data))
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+a.APIKey)
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := a.Client.Do(httpReq)
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			errCh <- fmt.Errorf("provider error (%d): %s", resp.StatusCode, body)
			return
		}

		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err != io.EOF {
					errCh <- err
				}
				break
			}

			line = strings.TrimSpace(line)
			if line == "" || !strings.HasPrefix(line, "data: ") {
				continue
			}

			dataStr := strings.TrimPrefix(line, "data: ")
			if dataStr == "[DONE]" {
				break
			}

			var streamResp models.ChatCompletionStreamResponse
			if err := json.Unmarshal([]byte(dataStr), &streamResp); err != nil {
				continue // 忽略损坏的行
			}
			respCh <- &streamResp
		}
	}()

	return respCh, errCh
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

// ChatCompletionStream 模拟流式响应。
func (a *MockAdapter) ChatCompletionStream(req *models.ChatCompletionRequest) (<-chan *models.ChatCompletionStreamResponse, <-chan error) {
	respCh := make(chan *models.ChatCompletionStreamResponse)
	errCh := make(chan error, 1)

	go func() {
		defer close(respCh)
		defer close(errCh)

		chunks := []string{"你好", "！", "我是", "一个", "来自", "网关", "的", "流式", "响应", "。"}
		for i, text := range chunks {
			respCh <- &models.ChatCompletionStreamResponse{
				ID:      fmt.Sprintf("mock-stream-%d", time.Now().Unix()),
				Created: time.Now().Unix(),
				Model:   req.Model,
				Choices: []models.StreamChoice{
					{
						Index: 0,
						Delta: models.ChoiceDelta{Content: text},
					},
				},
			}
			if i == len(chunks)-1 {
				respCh <- &models.ChatCompletionStreamResponse{
					Choices: []models.StreamChoice{{Index: 0, FinishReason: "stop"}},
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	return respCh, errCh
}
