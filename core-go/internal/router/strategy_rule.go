package router

import (
	"log/slog"
	"sort"
)

// Rule 描述一条可命中的路由规则。
// 当 Condition 返回 true 时，请求会优先路由到 Target 对应的节点。
type Rule struct {
	Name      string                       // 规则名称，便于日志和调试。
	Condition func(ctx *RouteContext) bool // 规则命中条件。
	Target    string                       // 目标节点名称。
	Priority  int                          // 优先级，数值越小越先匹配。
}

// RuleStrategy 按优先级依次匹配规则，第一条命中的规则决定路由目标。
type RuleStrategy struct {
	rules []Rule
}

// NewRuleStrategy 会先复制并排序规则，避免调用方后续修改切片影响运行时行为。
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
