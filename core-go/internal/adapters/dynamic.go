package adapters

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"text/template"
	"time"

	"github.com/ai-gateway/core/pkg/models"
)

// PluginConfig 描述一个通过 YAML 声明的动态适配器。
// 它允许我们在不重新编译 Go 代码的情况下，接入新的兼容 provider。
type PluginConfig struct {
	Name           string            `yaml:"name"`
	BaseURL        string            `yaml:"base_url"`
	DefaultHeaders map[string]string `yaml:"headers"`
	// RequestMapping 定义请求转换规则，目前支持 Header 模板和额外 body 字段注入。
	RequestMapping struct {
		HeaderTemplate map[string]string `yaml:"header_template"`
		BodyExtra      map[string]any    `yaml:"body_extra"`
	} `yaml:"request_mapping"`
}

// DynamicProtocol 实现了通过 YAML 配置定义的动态转换逻辑。
type DynamicProtocol struct {
	Plugin PluginConfig
}

func (p *DynamicProtocol) AuthHeaders(apiKey string) http.Header {
	headers := make(http.Header)
	// 允许插件通过模板渲染动态 Header，例如 Authorization: Bearer {{.Key}}。
	for k, tplStr := range p.Plugin.RequestMapping.HeaderTemplate {
		tmpl, err := template.New(k).Parse(tplStr)
		if err == nil {
			var b bytes.Buffer
			tmpl.Execute(&b, map[string]string{"Key": apiKey})
			headers.Set(k, b.String())
		}
	}
	// 补齐默认 Header。
	for k, v := range p.Plugin.DefaultHeaders {
		if headers.Get(k) == "" {
			headers.Set(k, v)
		}
	}
	return headers
}

func (p *DynamicProtocol) EncodeRequest(req *models.ChatCompletionRequest) ([]byte, http.Header, error) {
	// 先把标准 OpenAI 结构转成 map，再叠加插件要求的额外字段。
	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}
	var bodyMap map[string]any
	json.Unmarshal(reqData, &bodyMap)

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

func (p *DynamicProtocol) DecodeResponse(body io.Reader, statusCode int) (*models.ChatCompletionResponse, error) {
	if statusCode != http.StatusOK {
		b, _ := io.ReadAll(body)
		return nil, fmt.Errorf("dynamic plugin error (%d): %s", statusCode, string(b))
	}

	// 当前优先按通用 OpenAI 响应结构解码，后续可扩展 ResponseMapping。
	var result models.ChatCompletionResponse
	if err := json.NewDecoder(body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (p *DynamicProtocol) DecodeStreamChunk(line string) (*models.ChatCompletionStreamResponse, bool, error) {
	// 动态插件的流式协议还未统一，暂不提供默认实现。
	return nil, false, fmt.Errorf("dynamic plugin does not support streaming yet")
}

// NewDynamicAdapter 根据插件配置创建动态适配器，内部封装为 ProtocolAdapter。
func NewDynamicAdapter(p PluginConfig, apiKey string, timeout time.Duration) Provider {
	return NewProtocolAdapter(&DynamicProtocol{Plugin: p}, apiKey, p.BaseURL, timeout)
}

