// Package router 提供 AI 模型节点的智能路由与多策略选择引擎。
package router

import "github.com/ai-gateway/core/internal/adapters"

// ModelNode 描述一个可路由的 AI 模型节点及其元数据。
// 它是路由决策的基本单元，携带了成本、质量、标签等丰富信息。
type ModelNode struct {
	Name      string            // 节点标识（如 "gpt-4-primary"）。
	ModelID   string            // 模型家族标识（如 "gpt-4", "claude-3"）。
	Adapter   adapters.Provider // 实际执行适配器。
	Weight    int               // A/B 测试权重（默认 100）。
	CostPer1K float64          // 每 1K Token 的费用（美元）。
	Quality   float64          // 质量评分（0.0 ~ 1.0, 1.0 为最高）。
	Tags      map[string]string // 自由标签（如 "tier":"premium", "region":"us-east"）。
	Enabled   bool             // 是否启用（禁用的节点不参与路由）。
}

// Tag 安全地从 Tags 中读取标签值，若不存在则返回空字符串。
func (n *ModelNode) Tag(key string) string {
	if n.Tags == nil {
		return ""
	}
	return n.Tags[key]
}
