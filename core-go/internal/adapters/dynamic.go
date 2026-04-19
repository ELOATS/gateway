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

// PluginConfig 描述一个通过外部 YAML 声明的动态适配器配置。
//
// 设计方案：
// 它允许系统管理员在不修改代码且不重新编译的前提下，通过配置文件动态接入与 OpenAI 协议高度兼容的新供应商。
// 该配置描述了 API 基址、必要的静态头部，以及如何将授权令牌注入到特定的 HTTP 请求中。
type PluginConfig struct {
	Name           string            `yaml:"name"`           // 插件唯一标识名
	BaseURL        string            `yaml:"base_url"`       // 目标服务的 API 基准地址
	DefaultHeaders map[string]string `yaml:"headers"`        // 需要在每个请求中强制携带的静态头部
	// RequestMapping 定义了标准请求到自定义供应商格式的映射模板。
	RequestMapping struct {
		HeaderTemplate map[string]string `yaml:"header_template"` // Header 模板，支持 {{.Key}} 占位符进行鉴权令牌注入
		BodyExtra      map[string]any    `yaml:"body_extra"`      // 目标 Body 中需要额外注入的字段（如特定的 version 或 model 标识）
		StreamFormat   string            `yaml:"stream_format"`   // 响应流格式，当前仅支持 "sse"
	} `yaml:"request_mapping"`
}

// DynamicProtocol 实现了通过 YAML 配置定义的动态转换逻辑。
type DynamicProtocol struct {
	Plugin PluginConfig
}

// AuthHeaders 根据插件模板生成鉴权用的 HTTP 头部。
func (p *DynamicProtocol) AuthHeaders(apiKey string) http.Header {
	headers := make(http.Header)
	// 动态解析：允许插件将 API Key 灵活注入到各种 Header Key 中。
	// 例如：{"Authorization": "Bearer {{.Key}}"} 或 {"api-key": "{{.Key}}"}。
	for k, tplStr := range p.Plugin.RequestMapping.HeaderTemplate {
		rendered := strings.ReplaceAll(tplStr, "{{.Key}}", apiKey)
		headers.Set(k, rendered)
	}
	// 混合逻辑：如果用户配置了 DefaultHeaders 且其中包含非冲突字段，则叠加注入。
	for k, v := range p.Plugin.DefaultHeaders {
		if headers.Get(k) == "" {
			headers.Set(k, v)
		}
	}
	return headers
}

// EncodeRequest 讲网关标准请求体转换为插件要求的 JSON 格式。
func (p *DynamicProtocol) EncodeRequest(ctx context.Context, req *models.ChatCompletionRequest) ([]byte, http.Header, error) {
	// 先把标准 OpenAI 结构转成 map，再叠加插件要求的额外字段。
	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}
	var bodyMap map[string]any
	json.Unmarshal(reqData, &bodyMap)

	// 注入 BodyExtra 中定义的额外业务字段（例如租户 ID 或版本标识）。
	for k, v := range p.Plugin.RequestMapping.BodyExtra {
		bodyMap[k] = v
	}

	finalData, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, nil, err
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if req.Stream {
		headers.Set("Accept", "text/event-stream")
	} else {
		headers.Set("Accept", "application/json")
	}

	return finalData, headers, nil
}

// DecodeResponse 解析下游提供商的原始响应，通常假设其符合 OpenAI 标准响应格式。
func (p *DynamicProtocol) DecodeResponse(ctx context.Context, body io.Reader, statusCode int) (*models.ChatCompletionResponse, error) {
	if statusCode != http.StatusOK {
		b, _ := io.ReadAll(body)
		return nil, fmt.Errorf("dynamic plugin error (%d): %s", statusCode, string(b))
	}

	// 映射策略：当前动态适配器强制遵循 OpenAI 标准响应。
	// 如果目标供应商响应结构不标准，应通过修改 ResponseMapping 字段进行逻辑扩展（TODO）。
	var result models.ChatCompletionResponse
	if err := json.NewDecoder(body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// DecodeStreamChunk 以 SSE (Server-Sent Events) 方式解析流式响应。
func (p *DynamicProtocol) DecodeStreamChunk(line string) (*models.ChatCompletionStreamResponse, bool, error) {
	// 目前主要支持标准 SSE 格式 (OpenAI 兼容)
	format := p.Plugin.RequestMapping.StreamFormat
	if format == "" || format == "sse" {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			return nil, false, nil
		}
		dataStr := strings.TrimPrefix(line, "data: ")
		if dataStr == "[DONE]" {
			return nil, true, nil
		}
		var streamResp models.ChatCompletionStreamResponse
		if err := json.Unmarshal([]byte(dataStr), &streamResp); err != nil {
			// 跳过无法解析的正文部分，保证流的健壮性。
			return nil, false, nil
		}
		return &streamResp, false, nil
	}

	return nil, false, fmt.Errorf("unsupported dynamic stream format: %s", format)
}

// NewDynamicAdapter 根据插件配置创建动态适配器，内部封装为 ProtocolAdapter。
func NewDynamicAdapter(p PluginConfig, apiKey string, timeout time.Duration) Provider {
	return NewProtocolAdapter(&DynamicProtocol{Plugin: p}, apiKey, p.BaseURL, timeout)
}
