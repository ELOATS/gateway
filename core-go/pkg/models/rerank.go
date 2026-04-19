package models

// RerankRequest 表示网关标准的重排序请求模型。
// 该模型参考了 Cohere Rerank API 规范，旨在为 RAG（检索增强生成）流程提供精准的语义二次排序。
type RerankRequest struct {
	Model           string   `json:"model"`             // 目标逻辑模型名称
	Query           string   `json:"query"`             // 检索用户的原始提问
	Documents       []string `json:"documents"`         // 待排序的候选文档列表
	TopN            int      `json:"top_n,omitempty"`  // 返回前 N 个最相关的结果，默认为全部
	ReturnDocuments bool     `json:"return_documents,omitempty"` // 是否在响应中返回原始文档内容
}

// RerankResponse 表示重排序操作的最终聚合结果。
type RerankResponse struct {
	ID      string         `json:"id,omitempty"`      // 供应商侧的请求标识符
	Results []RerankResult `json:"results"`           // 经排序后的文档分值列表
	Meta    Usage          `json:"meta,omitempty"`    // 相关的资源消耗元数据
}

// RerankResult 描述了单个候选文档经过语义评估后的相关度得分。
type RerankResult struct {
	Index          int     `json:"index"`           // 文档在原始输入请求中的原始索引位置
	RelevanceScore float64 `json:"relevance_score"` // 语义相关度得分（通常分值越高越相关）
	Document       string  `json:"document,omitempty"` // 如果请求中要求返回，则此处包含文档原始内容
}
