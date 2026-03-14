package router

// CostStrategy 在满足最低质量要求的前提下，选择单价最低的模型节点。
// 如果没有节点满足质量下限，则退化为选择质量最高的节点。
type CostStrategy struct {
	MinQuality float64 // 质量下限（默认 0.0，即不限制）。
}

func (s *CostStrategy) Name() string { return "cost" }

func (s *CostStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	// 1. 筛选满足质量下限的节点。
	var qualified []*ModelNode
	for _, n := range nodes {
		if n.Quality >= s.MinQuality {
			qualified = append(qualified, n)
		}
	}

	// 2. 若无节点满足质量要求，退化为选择质量最高的节点。
	if len(qualified) == 0 {
		return selectByHighestQuality(nodes)
	}

	// 3. 在合格节点中选择成本最低的。
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
