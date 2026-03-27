package router

// QualityStrategy 优先选择质量评分最高的健康节点。
// 适用于对回答质量更敏感、愿意牺牲一部分成本和延迟的场景。
type QualityStrategy struct {
	Tracker *HealthTracker
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

	if bestHealthy != nil {
		return bestHealthy
	}

	return selectByHighestQuality(nodes)
}
