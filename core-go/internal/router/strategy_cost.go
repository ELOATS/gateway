package router

import "log/slog"

// CostStrategy 实现了成本感知与上下文长度感知的智能路由。
// 1. 如果用户等级为 "free" 且 PromptTokens > 1500，强制选择成本最低的模型（忽略质量要求），保护成本。
// 2. 否则，在满足最低质量要求的前提下，选择成本最低的模型。
type CostStrategy struct {
	MinQuality float64 // 质量下限（默认 0.0，即不限制）。
}

func (s *CostStrategy) Name() string { return "cost" }

func (s *CostStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	// 1. 成本保护: 判断是否需要强制降级 (免费用户且输入过长)
	forceCheap := ctx.UserTier == "free" && ctx.PromptTokens > 1500

	if forceCheap {
		slog.Warn("触发成本保护逻辑，强制降级路由", "request_id", ctx.RequestID, "tokens", ctx.PromptTokens)
	}

	// 2. 筛选满足质量下限的节点
	var qualified []*ModelNode
	for _, n := range nodes {
		// 如果必须降低成本，则取消最小质量限制
		if forceCheap || n.Quality >= s.MinQuality {
			qualified = append(qualified, n)
		}
	}

	// 3. 若无节点满足质量要求，且未强制降本，则退化为选择质量最高的节点
	if len(qualified) == 0 {
		return selectByHighestQuality(nodes)
	}

	// 4. 在参与比较的节点中，选择 1K Token 费用最低的节点
	best := qualified[0]
	for _, n := range qualified[1:] {
		if n.CostPer1K < best.CostPer1K {
			best = n
		}
	}

	return best
}

// selectByHighestQuality 从节点列表中选择质量评分最高的节点。
func selectByHighestQuality(nodes []*ModelNode) *ModelNode {
	best := nodes[0]
	for _, n := range nodes[1:] {
		if n.Quality > best.Quality {
			best = n
		}
	}
	return best
}
