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

// Provider 定义了不同 AI 生成服务供应商（如 OpenAI, Anthropic）需要实现的统一调用接口。
// 这套接口确保网关的 Pipeline 层可以忽略具体的供应商协议差异，实现高度统一的编排逻辑。
type Provider interface {
	// ChatCompletion 执行同步的聊天补全请求。
	ChatCompletion(ctx context.Context, req *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error)
	// ChatCompletionStream 执行流式补全，并通过 Channel 返回 SSE 数据块或错误。
	ChatCompletionStream(ctx context.Context, req *models.ChatCompletionRequest) (<-chan *models.ChatCompletionStreamResponse, <-chan error)
}

// ProviderType 用于标识受支持的供应商类型。
type ProviderType string

const (
	OpenAI    ProviderType = "openai"
	Anthropic ProviderType = "anthropic"
	Gemini    ProviderType = "gemini"
	Ollama    ProviderType = "ollama"
	DeepSeek  ProviderType = "deepseek"
	Qwen      ProviderType = "qwen"
	Mock      ProviderType = "mock"
	Plugin    ProviderType = "plugin" // 指向通过配置加载的动态适配器。
)

// Config 包含创建供应商适配器所需的元数据与网络参数。
type Config struct {
	Type       ProviderType
	APIKey     string
	URL        string
	Timeout    time.Duration
	Name       string // 多用于 Mock 场景，区分不同的模拟节点。
	PluginName string // 动态插件的逻辑名称，对应配置文件。
}

// NewProvider 是供应商对象的统一工厂方法。
// 设计原则：通过单一入口封装不同供应商的构造细节，包括协议转换器的注入。
func NewProvider(cfg Config) (Provider, error) {
	switch cfg.Type {
	case OpenAI:
		return NewProtocolAdapter(&OpenAIProtocol{}, cfg.APIKey, cfg.URL, cfg.Timeout), nil
	case Anthropic:
		return NewProtocolAdapter(&AnthropicProtocol{}, cfg.APIKey, cfg.URL, cfg.Timeout), nil
	case Gemini:
		return NewProtocolAdapter(&GeminiProtocol{}, cfg.APIKey, cfg.URL, cfg.Timeout), nil
	case Ollama:
		return NewProtocolAdapter(&OllamaProtocol{}, cfg.APIKey, cfg.URL, cfg.Timeout), nil
	case DeepSeek:
		return NewProtocolAdapter(NewOpenAICompatibleProtocol("deepseek"), cfg.APIKey, cfg.URL, cfg.Timeout), nil
	case Qwen:
		return NewProtocolAdapter(NewOpenAICompatibleProtocol("qwen"), cfg.APIKey, cfg.URL, cfg.Timeout), nil
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

// ProtocolAdapter 是基于 ProviderProtocol 接口实现的通用 HTTP 适配器。
// 设计哲学：它完全不感知具体的供应商身份，所有编解码（Encode/Decode）以及鉴权规则（AuthHeaders）
// 全部委托给注入的 Protocol 实例处理。它的职责仅限于执行网络层交互与流式管道管理。
//
// 这种模式实现了“一套代码驱动所有主流 LLM”的通用模型转换能力。
type ProtocolAdapter struct {
	Protocol ProviderProtocol // 注入的具体供应商协议解析逻辑
	APIKey   string           // 供应商 API Key
	URL      string           // 目标 API 终结点地址
	Client   *http.Client     // 复用的 HTTP 高性能客户端
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

// ChatCompletion 发起一次同步的对话请求。
// 执行流：编码请求 -> 注入鉴权头 -> 发送 HTTP -> 解码响应。
func (a *ProtocolAdapter) ChatCompletion(ctx context.Context, req *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
	data, headers, err := a.Protocol.EncodeRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.URL, bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	// 头部注入逻辑：
	// 1. 设置协议要求的通用 Header（如 Content-Type, Accept 等）。
	// 2. 注入鉴权 Header（如 Authorization: Bearer）。
	// 确保鉴权头最后注入，防止被协议层默认值覆盖。
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

// ChatCompletionStream 处理具有实时性要求的流式请求。
// 它通过双通道（数据通道与错误通道）将下游 SSE 消息异步推送给网关的上层组件。
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

				// 使用注入的协议解析器对 SSE 行进行解码
				streamResp, isDone, err := a.Protocol.DecodeStreamChunk(line)
				if err != nil {
					// 容错处理：对于无法识别或损坏的流数据行，我们选择跳过而非阻断整条流连接，提高鲁棒性。
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
