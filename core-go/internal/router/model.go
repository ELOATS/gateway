// Package router 提供模型节点路由与多策略选择能力。
package router

import "github.com/ai-gateway/core/internal/adapters"

// ModelNode 描述一个可执行的模型节点及其相关的策略元数据。
// 它是路由引擎决策的基本单元，包含了成本、质量、权重等影响路由权重的核心指标。
type ModelNode struct {
	Name      string            // 节点唯一名称（如：gpt-4-us-east-1）
	ModelID   string            // 模型标识符（如：gpt-4, claude-3），一个 ModelID 可能对应多个实体的 ModelNode
	Adapter   adapters.Provider // 执行实际 API 调用的供应商适配器
	Weight    int               // 权重值，主要用于加权轮询策略
	CostPer1K float64           // 每 1000 Tokens 的成本预算（用于成本优先策略）
	Quality   float64           // 质量分数，0.0-1.0（用于质量优先策略）
	Tags      map[string]string // 属性标签键值对，用于基于规则的精细化筛选
	Enabled   bool              // 节点启用状态控制
}

// Tag 是读取节点标签的辅助方法，内部进行了 nil 检查以提高调用方的健壮性。
func (n *ModelNode) Tag(key string) string {
	if n.Tags == nil {
		return ""
	}
	return n.Tags[key]
}
