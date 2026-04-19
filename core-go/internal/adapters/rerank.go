package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ai-gateway/core/pkg/models"
)

// RerankProvider 定义了重排序（Reranking）服务的标准接口。
// 重排序服务通常用于 RAG 检索流程，对初筛出的文档进行精准的语义排序。
type RerankProvider interface {
	Rerank(ctx context.Context, req *models.RerankRequest) (*models.RerankResponse, error)
}

// RerankProtocol 定义了“如何将网关标准 Rerank 请求转换为供应商原生格式”的解析逻辑。
type RerankProtocol interface {
	EncodeRequest(ctx context.Context, req *models.RerankRequest) ([]byte, http.Header, error)
	DecodeResponse(ctx context.Context, body io.Reader, statusCode int) (*models.RerankResponse, error)
	AuthHeaders(apiKey string) http.Header
}

// RerankAdapter 是 RerankProtocol 的通用 HTTP 传输实现。
// 类似于 ProtocolAdapter，它将繁琐的编解码细节交给具体的 Protocol 实现，自身仅处理网络通信。
type RerankAdapter struct {
	Protocol RerankProtocol // 具体的 Rerank 协议解析器
	APIKey   string         // 鉴权使用的 API Key
	URL      string         // 服务端点
	Client   *http.Client   // HTTP 客户端
}

func NewRerankAdapter(protocol RerankProtocol, apiKey, url string) *RerankAdapter {
	return &RerankAdapter{
		Protocol: protocol,
		APIKey:   apiKey,
		URL:      url,
		Client:   &http.Client{},
	}
}

func (a *RerankAdapter) Rerank(ctx context.Context, req *models.RerankRequest) (*models.RerankResponse, error) {
	data, headers, err := a.Protocol.EncodeRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("encode rerank request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.URL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create rerank request: %w", err)
	}

	for k, v := range headers {
		httpReq.Header[k] = v
	}
	for k, v := range a.Protocol.AuthHeaders(a.APIKey) {
		httpReq.Header[k] = v
	}

	resp, err := a.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute rerank request: %w", err)
	}
	defer resp.Body.Close()

	return a.Protocol.DecodeResponse(ctx, resp.Body, resp.StatusCode)
}

// CohereRerankProtocol 实现了 Cohere Rerank API 协议。
type CohereRerankProtocol struct{}

func (p *CohereRerankProtocol) AuthHeaders(apiKey string) http.Header {
	h := make(http.Header)
	h.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	return h
}

func (p *CohereRerankProtocol) EncodeRequest(ctx context.Context, req *models.RerankRequest) ([]byte, http.Header, error) {
	data, err := json.Marshal(req)
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	return data, h, err
}

func (p *CohereRerankProtocol) DecodeResponse(ctx context.Context, body io.Reader, statusCode int) (*models.RerankResponse, error) {
	if statusCode != http.StatusOK {
		b, _ := io.ReadAll(body)
		return nil, fmt.Errorf("cohere error (%d): %s", statusCode, string(b))
	}
	var res models.RerankResponse
	if err := json.NewDecoder(body).Decode(&res); err != nil {
		return nil, err
	}
	return &res, nil
}

// JinaRerankProtocol 实现了 Jina Rerank API 协议。
type JinaRerankProtocol struct{}

func (p *JinaRerankProtocol) AuthHeaders(apiKey string) http.Header {
	h := make(http.Header)
	h.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
	return h
}

func (p *JinaRerankProtocol) EncodeRequest(ctx context.Context, req *models.RerankRequest) ([]byte, http.Header, error) {
	data, err := json.Marshal(req)
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	return data, h, err
}

func (p *JinaRerankProtocol) DecodeResponse(ctx context.Context, body io.Reader, statusCode int) (*models.RerankResponse, error) {
	if statusCode != http.StatusOK {
		b, _ := io.ReadAll(body)
		return nil, fmt.Errorf("jina error (%d): %s", statusCode, string(b))
	}
	var res models.RerankResponse
	if err := json.NewDecoder(body).Decode(&res); err != nil {
		return nil, err
	}
	return &res, nil
}
