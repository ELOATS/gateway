package nitro

import "context"

// NitroClient 定义了加速层客户端的统一接口，支持 gRPC 和 Wasm 两种实现。
type NitroClient interface {
	CheckInput(ctx context.Context, prompt string) (string, error)
	CountTokens(ctx context.Context, model, text string) (int, error)
	Close() error
}
