package nitro

import "context"

// NitroClient 定义 Nitro 能力层的统一接口。
// 当前支持 gRPC 和 Wasm 两种载体，但两者对上层必须保持一致语义。
type NitroClient interface {
	CheckInput(ctx context.Context, prompt string) (string, error)
	CountTokens(ctx context.Context, model, text string) (int, error)
	Close() error
}
