package main

import (
	"context"
	"os"
	"testing"

	"github.com/ai-gateway/core/internal/nitro"
)

func TestSmokeV2(t *testing.T) {
	t.Log("=== AI Gateway smoke test (NITRO 2.0 + distributed base) ===")

	ctx := context.Background()
	wasmPath := "wasm/nitro.wasm"
	rules := `[{"pattern": "secret_key_[0-9]+", "replacement": "[KEY_HIDDEN]"}]`

	if _, err := os.Stat(wasmPath); os.IsNotExist(err) {
		t.Skip("skipping smoke test because wasm artifact is missing")
	}

	wasmClient, err := nitro.NewWasmNitroClient(ctx, wasmPath, rules)
	if err != nil {
		t.Fatalf("failed to create wasm client: %v", err)
	}
	defer wasmClient.Close()

	sanitized, err := wasmClient.CheckInput(ctx, "My access is secret_key_999.")
	if err != nil || sanitized != "My access is [KEY_HIDDEN]." {
		t.Fatalf("unexpected sanitized output: %q (err=%v)", sanitized, err)
	}

	count, err := wasmClient.CountTokens(ctx, "gpt-4", "Hello AI World!")
	if err != nil || count <= 0 {
		t.Fatalf("unexpected token count: %d (err=%v)", count, err)
	}
}
