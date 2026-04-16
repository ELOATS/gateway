package adapters

import (
	"context"
	"io"
	"net/http"

	"github.com/ai-gateway/core/pkg/models"
)

// ProviderProtocol 定义"如何把网关标准请求转成 provider 原生请求"的双向映射。
// 它是一个无状态的应用层转换器，只负责协议结构的映射，不负责网络传输。
//
// 设计决策：AuthHeaders 被纳入协议接口而非保留在 Adapter 中，
// 因为不同 provider 的鉴权方式是协议层的差异（OpenAI 用 Bearer，Anthropic 用 x-api-key），
// 而非传输层的差异。这让 Adapter 可以完全不感知 provider 身份。
type ProviderProtocol interface {
	// EncodeRequest 将网关标准请求编码为 provider 原生格式的数据流。
	EncodeRequest(ctx context.Context, req *models.ChatCompletionRequest) ([]byte, http.Header, error)

	// DecodeResponse 将 provider 原生响应解码为网关标准格式。
	DecodeResponse(ctx context.Context, body io.Reader, statusCode int) (*models.ChatCompletionResponse, error)

	// DecodeStreamChunk 将 provider 原生流式 chunk 解码为网关标准流数据块。
	// isDone 返回 true 表示收到流结束信号。
	DecodeStreamChunk(line string) (resp *models.ChatCompletionStreamResponse, isDone bool, err error)

	// AuthHeaders 返回 provider 特有的鉴权头。
	// 例如 OpenAI 返回 {Authorization: Bearer <key>}，Anthropic 返回 {x-api-key: <key>}。
	AuthHeaders(apiKey string) http.Header
}
