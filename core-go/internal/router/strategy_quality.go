package router

// QualityStrategy 实现质量优先的选择策略。
// 它适用于对回答质量有极高要求的场景（如代码生成、复杂推理）。该算法会优先选择 Quality 指标最高的健康节点，
// 即便该节点的成本或延迟可能高于其他节点。
type QualityStrategy struct {
	Tracker *HealthTracker // 可选：用于排除当前出现故障的高质量节点
}

func (s *QualityStrategy) Name() string { return "quality" }

func (s *QualityStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	var bestHealthy *ModelNode
	for _, n := range nodes {
		if !s.Tracker.IsHealthy(n.Name) {
			continue
		}
		if bestHealthy == nil || n.Quality > bestHealthy.Quality {
			bestHealthy = n
		}
	}

	// 容灾处理：如果所有的高质量节点目前都不健康，则在全量节点中寻找质量评分最高的一个作为兜底。
	return selectByHighestQuality(nodes)
}
