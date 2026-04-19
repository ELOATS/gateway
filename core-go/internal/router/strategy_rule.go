package router

import (
	"log/slog"
	"sort"
)

// Rule 描述一条静态路由规则。
// 当 Condition 返回 true 时，流量会强制导向 Target 指定的物理节点名。
type Rule struct {
	Name      string                       // 规则描述名称，用于审计日志输出。
	Condition func(ctx *RouteContext) bool // 匹配函数，可根据 UserTier、Prompt 等字段编写。
	Target    string                       // 命中后强制选中的后端节点名称。
	Priority  int                          // 执行优先级，数值越小越优先判定（0 为最高）。
}

// RuleStrategy 实现基于业务规则的强制路由（IF-THEN 逻辑）。
// 此策略适用于处理特例请求，如：特定用户强制使用 GPT-4，或特定 Prompt 强制由本地 Llama 处理。
type RuleStrategy struct {
	rules []Rule
}

// NewRuleStrategy 构造并根据优先级预对规则进行排序。
// 设计意图：预排序可以极大地提高单次 Route 调用的性能，避免在主请求路径上重复排序。
func NewRuleStrategy(rules []Rule) *RuleStrategy {
	sorted := make([]Rule, len(rules))
	copy(sorted, rules)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority < sorted[j].Priority
	})
	return &RuleStrategy{rules: sorted}
}

func (s *RuleStrategy) Name() string { return "rule" }

func (s *RuleStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	index := make(map[string]*ModelNode, len(nodes))
	for _, n := range nodes {
		index[n.Name] = n
	}

	for _, rule := range s.rules {
		if rule.Condition(ctx) {
			if target, ok := index[rule.Target]; ok {
				slog.Debug("路由规则命中", "rule", rule.Name, "target", rule.Target)
				return target
			}
			slog.Warn("路由规则命中但目标节点不存在", "rule", rule.Name, "target", rule.Target)
		}
	}

	return nil
}
