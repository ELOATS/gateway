package router

import "log/slog"

// CostStrategy 实现成本优化策略。
// 该算法在满足最低质量门槛（MinQuality）的前提下，优先选择计费价格最低的节点。
// 此外，它内置了“成本保护”机制：针对免费用户且上下文较长（消耗大）的请求，会强制路由到最廉价的节点，以控制系统开销。
type CostStrategy struct {
	MinQuality float64 // 最低质量阈值；若设为 0 则表示不考虑质量，仅选择最廉价节点。
}

func (s *CostStrategy) Name() string { return "cost" }

func (s *CostStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	// 成本保护机制：针对 Tier 为 "free" 且输入 Token 超过 1500 的请求。
	// 这样做是为了防止免费用户的大规模并发请求导致公司层面的 API 预算超支。
	forceCheap := ctx.UserTier == "free" && ctx.PromptTokens > 1500
	if forceCheap {
		slog.Warn("触发成本保护，强制选择低成本节点", "request_id", ctx.RequestID, "tokens", ctx.PromptTokens)
	}

	var qualified []*ModelNode
	for _, n := range nodes {
		if forceCheap || n.Quality >= s.MinQuality {
			qualified = append(qualified, n)
		}
	}

	// 容灾处理：如果没有节点能满足设定的最低质量门槛。
	// 为了保证服务始终有输出，我们将算法降级为“在存活节点中寻找质量最高的节点”。
	if len(qualified) == 0 {
		return selectByHighestQuality(nodes)
	}

	best := qualified[0]
	for _, n := range qualified[1:] {
		if n.CostPer1K < best.CostPer1K {
			best = n
		}
	}

	return best
}

// selectByHighestQuality 作为策略回退逻辑，在各种复杂路由算法无法收敛时提供最稳定的质量兜底方案。
func selectByHighestQuality(nodes []*ModelNode) *ModelNode {
	best := nodes[0]
	for _, n := range nodes[1:] {
		if n.Quality > best.Quality {
			best = n
		}
	}
	return best
}
