package adapters

import (
	"context"
	"io"
	"net/http"

	"github.com/ai-gateway/core/pkg/models"
)

// ProviderProtocol 定义了“如何将网关标准协议转换为下游供应商原生协议”的双向映射。
// 它是一个纯粹的、无状态的应用层转换协议栈。设计上它只负责 Data Struct 的映射转换，而不关心具体的 HTTP 网络传输。
//
// 核心设计决策：
// 鉴权头部（AuthHeaders）被纳入 ProviderProtocol 接口而非 Adapter，
// 这是因为不同供应商（如 OpenAI 使用 Bearer Token，Anthropic 使用 x-api-key）的差异本质上是协议格式的差异。
// 这种解耦使得底层的 Adapter (HTTP Transport) 可以保持高度通用，完全不需要感知具体对接的是哪一家供应商。
type ProviderProtocol interface {
	// EncodeRequest 将网关标准的 ChatCompletionRequest 结构体编码为目标供应商识别的二进制数据流及 HTTP 头。
	EncodeRequest(ctx context.Context, req *models.ChatCompletionRequest) ([]byte, http.Header, error)

	// DecodeResponse 将供应商返回的原生响应 Body 解码为网关标准的 ChatCompletionResponse。
	DecodeResponse(ctx context.Context, body io.Reader, statusCode int) (*models.ChatCompletionResponse, error)

	// DecodeStreamChunk 将供应商推送的原生流式数据块（SSE Line）解码为网关标准的流响应。
	// 当 isDone 为 true 时，表示流已正常结束。
	DecodeStreamChunk(line string) (resp *models.ChatCompletionStreamResponse, isDone bool, err error)

	// AuthHeaders 构建并返回供应商特定的鉴权 HTTP 头部。
	// 例如：OpenAI -> {"Authorization": "Bearer ..."}；Anthropic -> {"x-api-key": "..."}。
	AuthHeaders(apiKey string) http.Header
}
