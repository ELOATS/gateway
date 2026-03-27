package router

import "log/slog"

// CostStrategy 在满足质量下限的前提下优先选择成本更低的节点。
// 对免费用户的大上下文请求会触发成本保护，直接偏向最便宜的模型。
type CostStrategy struct {
	MinQuality float64 // 最低质量门槛；为 0 时表示不限制。
}

func (s *CostStrategy) Name() string { return "cost" }

func (s *CostStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	// 免费用户且上下文较大时优先保成本，避免单次请求异常昂贵。
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

	// 如果没有节点满足质量门槛，则退化到“选质量最高的节点”。
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

// selectByHighestQuality 在无法满足成本策略约束时，提供一个稳定的高质量兜底选择。
func selectByHighestQuality(nodes []*ModelNode) *ModelNode {
	best := nodes[0]
	for _, n := range nodes[1:] {
		if n.Quality > best.Quality {
			best = n
		}
	}
	return best
}
