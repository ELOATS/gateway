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

// DynamicAdapter 是基于 PluginConfig 构造出来的 provider 适配器实例。
type DynamicAdapter struct {
	Plugin PluginConfig
	APIKey string
	Client *http.Client
}

// NewDynamicAdapter 根据插件配置创建动态适配器。
func NewDynamicAdapter(p PluginConfig, apiKey string, timeout time.Duration) *DynamicAdapter {
	return &DynamicAdapter{
		Plugin: p,
		APIKey: apiKey,
		Client: &http.Client{Timeout: timeout},
	}
}

func (a *DynamicAdapter) ChatCompletion(req *models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
	// 先把标准 OpenAI 结构转成 map，再叠加插件要求的额外字段。
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

	// 允许插件通过模板渲染动态 Header，例如 Authorization: Bearer {{.Key}}。
	for k, tplStr := range a.Plugin.RequestMapping.HeaderTemplate {
		tmpl, err := template.New(k).Parse(tplStr)
		if err == nil {
			var b bytes.Buffer
			tmpl.Execute(&b, map[string]string{"Key": a.APIKey})
			httpReq.Header.Set(k, b.String())
		}
	}

	// 若插件未显式覆盖，则补齐默认 Header。
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

	// 当前优先按通用 OpenAI 响应结构解码，后续可扩展 ResponseMapping。
	var result models.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (a *DynamicAdapter) ChatCompletionStream(req *models.ChatCompletionRequest) (<-chan *models.ChatCompletionStreamResponse, <-chan error) {
	// 动态插件的流式协议还未统一，因此先显式返回“不支持”，避免假装兼容。
	errCh := make(chan error, 1)
	errCh <- fmt.Errorf("dynamic plugin %s does not support streaming yet", a.Plugin.Name)
	return nil, errCh
}
