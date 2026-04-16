// Package adapters 提供各类 AI 服务提供商的适配实现。
package adapters

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ai-gateway/core/pkg/models"
)

// Provider 定义不同 AI 提供者需要实现的统一调用接口。
type Provider interface {
	ChatCompletion(ctx context.Context, req *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error)
	ChatCompletionStream(ctx context.Context, req *models.ChatCompletionRequest) (<-chan *models.ChatCompletionStreamResponse, <-chan error)
}

// ProviderType 表示当前支持的 provider 类型。
type ProviderType string

const (
	OpenAI    ProviderType = "openai"
	Anthropic ProviderType = "anthropic"
	Mock      ProviderType = "mock"
	Plugin    ProviderType = "plugin" // 动态插件 provider。
)

// Config 包含创建 provider 适配器所需的配置信息。
type Config struct {
	Type       ProviderType
	APIKey     string
	URL        string
	Timeout    time.Duration
	Name       string // 仅供 MockAdapter 使用。
	PluginName string // 动态插件名称，对应 configs/adapters/*.yaml。
}

// NewProvider 根据配置创建对应的 provider 实例。
func NewProvider(cfg Config) (Provider, error) {
	switch cfg.Type {
	case OpenAI:
		return NewProtocolAdapter(&OpenAIProtocol{}, cfg.APIKey, cfg.URL, cfg.Timeout), nil
	case Anthropic:
		return NewProtocolAdapter(&AnthropicProtocol{}, cfg.APIKey, cfg.URL, cfg.Timeout), nil
	case Mock:
		return &MockAdapter{Name: cfg.Name}, nil
	case Plugin:
		plugin, ok := GlobalRegistry.GetPlugin(cfg.PluginName)
		if !ok {
			return nil, fmt.Errorf("plugin configuration not found: %s", cfg.PluginName)
		}
		// 若显式传入 URL，则优先覆盖插件默认地址，便于环境级重定向。
		if cfg.URL != "" {
			plugin.BaseURL = cfg.URL
		}
		return NewDynamicAdapter(plugin, cfg.APIKey, cfg.Timeout), nil
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", cfg.Type)
	}
}

// ProtocolAdapter 是基于 ProviderProtocol 的通用适配器。
// 它完全不感知 provider 身份——编码、解码和鉴权全部委托给 Protocol，
// 自身只做 HTTP 传输和流式管道管理。
//
// 这取代了之前的 OpenAIAdapter，让同一份传输代码服务于 OpenAI、Anthropic 等所有 provider。
type ProtocolAdapter struct {
	Protocol ProviderProtocol
	APIKey   string
	URL      string
	Client   *http.Client
}

// NewProtocolAdapter 创建通用协议适配器，配置连接复用参数。
func NewProtocolAdapter(protocol ProviderProtocol, apiKey, url string, timeout time.Duration) *ProtocolAdapter {
	return &ProtocolAdapter{
		Protocol: protocol,
		APIKey:   apiKey,
		URL:      url,
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

// NewOpenAIAdapter 向后兼容的便利构造函数。
// Deprecated: 新代码应直接使用 NewProtocolAdapter(&OpenAIProtocol{}, ...)。
func NewOpenAIAdapter(apiKey, url string, timeout time.Duration) *ProtocolAdapter {
	return NewProtocolAdapter(&OpenAIProtocol{}, apiKey, url, timeout)
}

// ChatCompletion 发起一次非流式聊天补全请求。
func (a *ProtocolAdapter) ChatCompletion(ctx context.Context, req *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
	data, headers, err := a.Protocol.EncodeRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.URL, bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	// 先设置协议层 header（Content-Type, Accept 等），
	// 再叠加 auth header，确保 auth 不被协议层覆盖。
	for k, v := range headers {
		httpReq.Header[k] = v
	}
	for k, v := range a.Protocol.AuthHeaders(a.APIKey) {
		httpReq.Header[k] = v
	}

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	result, err := a.Protocol.DecodeResponse(ctx, resp.Body, resp.StatusCode)
	if err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}

// ChatCompletionStream 发起 SSE 流式补全请求，并逐行转成标准 chunk 输出。
func (a *ProtocolAdapter) ChatCompletionStream(ctx context.Context, req *models.ChatCompletionRequest) (<-chan *models.ChatCompletionStreamResponse, <-chan error) {
	respCh := make(chan *models.ChatCompletionStreamResponse)
	errCh := make(chan error, 1)

	go func() {
		defer close(respCh)
		defer close(errCh)

		req.Stream = true
		data, headers, err := a.Protocol.EncodeRequest(ctx, req)
		if err != nil {
			errCh <- fmt.Errorf("encode request: %w", err)
			return
		}

		httpReq, _ := http.NewRequestWithContext(ctx, "POST", a.URL, bytes.NewBuffer(data))
		for k, v := range headers {
			httpReq.Header[k] = v
		}
		for k, v := range a.Protocol.AuthHeaders(a.APIKey) {
			httpReq.Header[k] = v
		}

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
			select {
			case <-ctx.Done():
				return
			default:
				line, err := reader.ReadString('\n')
				if err != nil {
					if err != io.EOF {
						errCh <- err
					}
					return
				}

				streamResp, isDone, err := a.Protocol.DecodeStreamChunk(line)
				if err != nil {
					// 上游偶发坏行不直接中断整条流
					continue
				}
				if isDone {
					return
				}
				if streamResp != nil {
					respCh <- streamResp
				}
			}
		}
	}()

	return respCh, errCh
}

// MockAdapter 用于本地开发、压测和无外部依赖场景。
type MockAdapter struct {
	Name string
}

// ChatCompletion 生成一个固定结构的模拟响应。
func (a *MockAdapter) ChatCompletion(ctx context.Context, req *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
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

// ChatCompletionStream 生成一个分块输出的模拟流式响应。
func (a *MockAdapter) ChatCompletionStream(ctx context.Context, req *models.ChatCompletionRequest) (<-chan *models.ChatCompletionStreamResponse, <-chan error) {
	respCh := make(chan *models.ChatCompletionStreamResponse)
	errCh := make(chan error, 1)

	go func() {
		defer close(respCh)
		defer close(errCh)

		chunks := []string{"你好", "，", "我是", "一个", "来自", "网关", "的", "流式", "响应", "。"}
		for i, text := range chunks {
			select {
			case <-ctx.Done():
				return
			default:
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
		}
	}()

	return respCh, errCh
}
