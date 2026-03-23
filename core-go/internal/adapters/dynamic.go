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

// PluginConfig 定义了通过 YAML 描述的适配器规范。
type PluginConfig struct {
	Name           string            `yaml:"name"`
	BaseURL        string            `yaml:"base_url"`
	DefaultHeaders map[string]string `yaml:"headers"`
	// RequestMapping 定义了如何转换 OpenAI 请求。目前支持自定义 Header 和额外参数注入。
	RequestMapping struct {
		HeaderTemplate map[string]string `yaml:"header_template"`
		BodyExtra     map[string]any    `yaml:"body_extra"`
	} `yaml:"request_mapping"`
}

// DynamicAdapter 是根据 PluginConfig 动态生成的适配器实例。
type DynamicAdapter struct {
	Plugin PluginConfig
	APIKey string
	Client *http.Client
}

// NewDynamicAdapter 基于插件配置创建一个动态适配器。
func NewDynamicAdapter(p PluginConfig, apiKey string, timeout time.Duration) *DynamicAdapter {
	return &DynamicAdapter{
		Plugin: p,
		APIKey: apiKey,
		Client: &http.Client{Timeout: timeout},
	}
}

func (a *DynamicAdapter) ChatCompletion(req *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
	// 1. 构造请求体：目前支持在透传 OpenAI 结构的基础上注入 body_extra。
	reqData, _ := json.Marshal(req)
	var bodyMap map[string]any
	json.Unmarshal(reqData, &bodyMap)
	
	for k, v := range a.Plugin.RequestMapping.BodyExtra {
		bodyMap[k] = v
	}
	
	finalData, _ := json.Marshal(bodyMap)
	
	httpReq, err := http.NewRequest("POST", a.Plugin.BaseURL, bytes.NewBuffer(finalData))
	if err != nil {
		return nil, err
	}
	
	// 2. 注入动态 Header。支持模板渲染，如 Authorization: Bearer {{.Key}}
	for k, tplStr := range a.Plugin.RequestMapping.HeaderTemplate {
		tmpl, err := template.New(k).Parse(tplStr)
		if err == nil {
			var b bytes.Buffer
			tmpl.Execute(&b, map[string]string{"Key": a.APIKey})
			httpReq.Header.Set(k, b.String())
		}
	}
	
	// 补全默认 Header
	for k, v := range a.Plugin.DefaultHeaders {
		if httpReq.Header.Get(k) == "" {
			httpReq.Header.Set(k, v)
		}
	}
	
	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("plugin %s error (%d): %s", a.Plugin.Name, resp.StatusCode, body)
	}
	
	// 3. 解析响应：目前暂定优先解析为通用 OpenAI 结构。
	// 下一步可增加 ResponseMapping 逻辑。
	var result models.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (a *DynamicAdapter) ChatCompletionStream(req *models.ChatCompletionRequest) (<-chan *models.ChatCompletionStreamResponse, <-chan error) {
	// [TBD] 流式目前复用 OpenAI 逻辑，后续可通过插件指定 Data Prefix 或 Splitter。
	// 为保持简单，暂抛出未实现或降级。
	errCh := make(chan error, 1)
	errCh <- fmt.Errorf("dynamic plugin %s does not support streaming yet", a.Plugin.Name)
	return nil, errCh
}
