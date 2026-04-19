package adapters

import (
	"context"
	"net/http"

	"github.com/ai-gateway/core/pkg/models"
)

// OpenAICompatibleProtocol 这是一个通用的 OpenAI 兼容协议实现。
// 它复用了 OpenAIProtocol 的逻辑，但允许通过 ProviderType 进行区分（如果需要特殊的 Header）。
type OpenAICompatibleProtocol struct {
	OpenAIProtocol
	ProviderName string
}

func NewOpenAICompatibleProtocol(providerName string) *OpenAICompatibleProtocol {
	return &OpenAICompatibleProtocol{
		ProviderName: providerName,
	}
}

func (p *OpenAICompatibleProtocol) AuthHeaders(apiKey string) http.Header {
	h := p.OpenAIProtocol.AuthHeaders(apiKey)
	// 在这里可以根据 ProviderName 添加特殊的 Header
	// 比如某些平台需要额外的 X-Provider-Key 等
	return h
}

func (p *OpenAICompatibleProtocol) EncodeRequest(ctx context.Context, req *models.ChatCompletionRequest) ([]byte, http.Header, error) {
	data, headers, err := p.OpenAIProtocol.EncodeRequest(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	// 可以在这里标记网关来源等信息
	headers.Set("X-Gateway-Provider", p.ProviderName)
	return data, headers, nil
}
