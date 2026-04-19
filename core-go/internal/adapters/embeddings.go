package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// EmbeddingRequest 表示网关标准的向量化请求结构，兼容大多数主流提供商。
type EmbeddingRequest struct {
	Model string `json:"model"` // 目标向量模型名称（如 text-embedding-3-small）
	Input string `json:"input"` // 需要转换为向量的原始文本
}

// EmbeddingResponse 表示网关标准的向量化响应解析结构。
type EmbeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"` // 生成的浮点数向量数组
	} `json:"data"`
}

// EmbeddingProvider 抽象了文本向量化能力。
// 它是网关“语义缓存”功能的核心依赖，负责将用户提问转化为可以进行相似度检索的向量。
type EmbeddingProvider interface {
	// Embed 将给定文本转换为高维向量。
	Embed(ctx context.Context, text string) ([]float32, error)
}

// OpenAIEmbeddingProtocol 实现了基于 OpenAI 兼容接口的文本向量化协议。
// 它主要用于“语义缓存”模块，将高频问题的文本转化为向量，以便在 Redis/内存中进行近似邻查询。
type OpenAIEmbeddingProtocol struct {
	APIKey string       // 供应商 API 访问密钥
	URL    string       // 向量化接口的完整地址
	Model  string       // 使用的向量模型（如 text-embedding-3-small）
	Client *http.Client // 复用的网络客户端
}

// NewOpenAIEmbeddingProvider 构建一个新的向量化提供者实例。
func NewOpenAIEmbeddingProvider(apiKey, url, model string) *OpenAIEmbeddingProtocol {
	return &OpenAIEmbeddingProtocol{
		APIKey: apiKey,
		URL:    url,
		Model:  model,
		Client: &http.Client{},
	}
}

// Embed 调用远端 API 将字符串转换为 1536 维（或其他维度）的浮点向量。
// 该操作通常用于请求进入网关时的第一阶段，以判断是否可以触发高性能缓存命中。
func (p *OpenAIEmbeddingProtocol) Embed(ctx context.Context, text string) ([]float32, error) {
	reqBody := EmbeddingRequest{
		Model: p.Model,
		Input: text,
	}
	data, _ := json.Marshal(reqBody)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.URL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Authorization", "Bearer " + p.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding error (%d): %s", resp.StatusCode, string(b))
	}

	var res EmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	if len(res.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	return res.Data[0].Embedding, nil
}
