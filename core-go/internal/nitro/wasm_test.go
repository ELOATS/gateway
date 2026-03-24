package nitro

import (
	"context"
	"os"
	"testing"
)

func TestWasmNitroClientIntegration(t *testing.T) {
	// 确保 Wasm 产物存在
	wasmPath := "../../wasm/nitro.wasm"
	if _, err := os.Stat(wasmPath); os.IsNotExist(err) {
		t.Skip("Wasm artifact not found, skipping integration test")
	}

	// 注入简单的规则：将 email 脱敏为 [EMAIL]
	rules := `[{"pattern": "([a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,})", "replacement": "[EMAIL]"}]`

	client, err := NewWasmNitroClient(context.Background(), wasmPath, rules)
	if err != nil {
		t.Fatalf("Failed to create Wasm client: %v", err)
	}
	defer client.Close()

	// 测试脱敏逻辑
	prompt := "Contact me at alice@example.com for details."
	expected := "Contact me at [EMAIL] for details."

	sanitized, err := client.CheckInput(context.Background(), prompt)
	if err != nil {
		t.Fatalf("CheckInput failed: %v", err)
	}

	if sanitized != expected {
		t.Errorf("Sanitization mismatch.\nGot:  %s\nWant: %s", sanitized, expected)
	}

	// 测试分词统计
	count, err := client.CountTokens(context.Background(), "gpt-4", "Hello world!")
	if err != nil {
		t.Fatalf("CountTokens failed: %v", err)
	}

	if count <= 0 {
		t.Errorf("CountTokens returned non-positive value: %d", count)
	}
}
