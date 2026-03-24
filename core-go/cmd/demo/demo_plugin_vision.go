package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/ai-gateway/core/internal/adapters"
	"github.com/ai-gateway/core/pkg/models"
)

func main() {
	fmt.Println("=== Phase 5.1: 动态插件与多模态功能演示 ===")

	// 1. 模拟一个异构的 AI Provider (例如模拟 Anthropic Claude 接口)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("\n[Provider 收到请求]\n")
		fmt.Printf("Header -> x-api-key: %s\n", r.Header.Get("x-api-key"))
		fmt.Printf("Header -> anthropic-version: %s\n", r.Header.Get("anthropic-version"))

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		fmt.Println("Body 内容解析:")
		fmt.Printf(" - Model: %v\n", body["model"])
		fmt.Printf(" - MaxTokens (from extra): %v\n", body["max_tokens"])

		msgs := body["messages"].([]any)
		last := msgs[len(msgs)-1].(map[string]any)
		content := last["content"].([]any)
		fmt.Printf(" - 检测到多模态内容段数: %d\n", len(content))
		for i, part := range content {
			p := part.(map[string]any)
			fmt.Printf("   [%d] Type: %s, Data: %v...\n", i, p["type"], p[p["type"].(string)])
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"[DEMO] 我已经收到了您的图片和文字，通过插件映射成功！"}}]}`))
	}))
	defer server.Close()

	// 2. 加载插件配置 (模拟在 configs/adapters 中的配置)
	plugin := adapters.PluginConfig{
		Name:    "demo-claude",
		BaseURL: server.URL,
		DefaultHeaders: map[string]string{
			"anthropic-version": "2023-06-01",
		},
	}
	plugin.RequestMapping.HeaderTemplate = map[string]string{
		"x-api-key": "{{.Key}}",
	}
	plugin.RequestMapping.BodyExtra = map[string]any{
		"max_tokens": 4096,
	}

	// 注册到全局（虽然 demo 直接用，但展示逻辑一致）
	adapters.GlobalRegistry.Plugins["demo-claude"] = plugin
	fmt.Printf("\n[网关状态] 已加载插件: %s, 目标地址: %s\n", plugin.Name, plugin.BaseURL)

	// 3. 构建多模态请求
	adapter := adapters.NewDynamicAdapter(plugin, "sk-anthropic-secret-123", 5*time.Second)
	req := &models.ChatCompletionRequest{
		Model: "claude-3-opus",
		Messages: []models.Message{
			{
				Role: "user",
				Content: []models.ContentPart{
					{Type: "text", Text: "分析一下这张图里的架构："},
					{Type: "image_url", ImageURL: &models.ContentPathImage{URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUGAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="}},
				},
			},
		},
	}

	fmt.Println("\n[网关处理] 正在执行多模态请求分发...")
	resp, err := adapter.ChatCompletion(req)
	if err != nil {
		slog.Error("演示失败", "error", err)
		return
	}

	fmt.Printf("\n[最终响应] Assistant: %s\n", resp.Choices[0].Message.GetText())
	fmt.Println("\n=== 演示结束: 完美支持动态映射与多模态负载 ===")
}
