// Package router 提供模型节点路由与多策略选择能力。
package router

import "github.com/ai-gateway/core/internal/adapters"

// ModelNode 描述一个可被路由层选中的模型节点及其元数据。
// 路由策略会基于成本、质量、权重、标签和健康状态等字段做决策。
type ModelNode struct {
	Name      string            // 节点名称，例如 gpt-4-primary。
	ModelID   string            // 模型家族标识，例如 gpt-4、claude-3。
	Adapter   adapters.Provider // 实际执行请求的 provider 适配器。
	Weight    int               // 灰度或 A/B 分流权重。
	CostPer1K float64           // 每 1K Token 的成本估计。
	Quality   float64           // 质量评分，范围通常为 0.0 到 1.0。
	Tags      map[string]string // 自定义标签，如 tier、region。
	Enabled   bool              // 是否参与路由。
}

// Tag 安全读取节点标签，避免调用方额外处理 nil map。
func (n *ModelNode) Tag(key string) string {
	if n.Tags == nil {
		return ""
	}
	return n.Tags[key]
}
