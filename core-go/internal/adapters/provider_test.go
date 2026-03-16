package adapters

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-gateway/core/pkg/models"
)

func TestOpenAIAdapter_ChatCompletion(t *testing.T) {
	// 1. 创建 Mock 服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"test-id","choices":[{"message":{"content":"Hello"}}]}`)
	}))
	defer server.Close()

	// 2. 初始化适配器
	adapter := NewOpenAIAdapter("test-key", server.URL, 5*time.Second)

	// 3. 执行测试
	req := &models.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []models.Message{
			{Role: "user", Content: "Hi"},
		},
	}
	resp, err := adapter.ChatCompletion(req)

	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if resp.Choices[0].Message.Content != "Hello" {
		t.Errorf("预期 Hello，实际 %s", resp.Choices[0].Message.Content)
	}
}

func TestOpenAIAdapter_ChatCompletionStream(t *testing.T) {
	// 1. 创建 Mock 服务器支持 SSE
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"H\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"i\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := NewOpenAIAdapter("test-key", server.URL, 5*time.Second)
	req := &models.ChatCompletionRequest{Model: "gpt-4"}
	
	respCh, errCh := adapter.ChatCompletionStream(req)

	var result string
	for {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("流错误: %v", err)
			}
		case resp, ok := <-respCh:
			if !ok {
				goto DONE
			}
			result += resp.Choices[0].Delta.Content
		case <-time.After(1 * time.Second):
			t.Fatal("流超时")
		}
	}

DONE:
	if result != "Hi" {
		t.Errorf("预期 Hi，实际 %s", result)
	}
}

func BenchmarkOpenAI_StreamParsing(b *testing.B) {
	// 模拟一条标准的 SSE 数据行。
	line := "data: {\"id\":\"chatcmpl-123\",\"object\":\"chat.completion.chunk\",\"created\":1694268190,\"model\":\"gpt-4-0613\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello world\"},\"finish_reason\":null}]}\n"
	
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 10; i++ {
			fmt.Fprint(w, line)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := NewOpenAIAdapter("test-key", server.URL, 5*time.Second)
	req := &models.ChatCompletionRequest{Model: "gpt-4"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		respCh, _ := adapter.ChatCompletionStream(req)
		for range respCh {
			// 抽干内容
		}
	}
}
