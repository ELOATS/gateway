package router

import (
	"log/slog"
	"sort"
)

// Rule 定义一条路由匹配规则。
// 当 Condition 对路由上下文返回 true 时，请求将被路由到 Target 节点。
type Rule struct {
	Name      string                          // 规则名称（用于日志和调试）。
	Condition func(ctx *RouteContext) bool     // 匹配条件函数。
	Target    string                          // 目标节点名称。
	Priority  int                             // 优先级（数字越小越优先）。
}

// RuleStrategy 按优先级逐条匹配规则，第一条命中的规则决定路由目标。
// 如果没有规则命中，返回 nil，由上层回退到默认策略。
type RuleStrategy struct {
	rules []Rule
}

// NewRuleStrategy 创建规则路由策略并按优先级排序。
func NewRuleStrategy(rules []Rule) *RuleStrategy {
	// 按优先级升序排序（数字越小越先匹配）。
	sorted := make([]Rule, len(rules))
	copy(sorted, rules)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority < sorted[j].Priority
	})
	return &RuleStrategy{rules: sorted}
}

func (s *RuleStrategy) Name() string { return "rule" }

func (s *RuleStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	// 构建节点名称索引以快速查找。
	index := make(map[string]*ModelNode, len(nodes))
	for _, n := range nodes {
		index[n.Name] = n
	}

	// 按优先级逐条匹配。
	for _, rule := range s.rules {
		if rule.Condition(ctx) {
			if target, ok := index[rule.Target]; ok {
				slog.Debug("规则命中", "rule", rule.Name, "target", rule.Target)
				return target
			}
			slog.Warn("规则命中但目标节点不存在", "rule", rule.Name, "target", rule.Target)
		}
	}

	return nil // 无规则命中，交由上层处理。
}
