package router

// QualityStrategy 直接选择质量评分最高的健康节点。
// 适用于对响应质量要求极高的场景（如法律、医疗咨询）。
type QualityStrategy struct {
	Tracker *HealthTracker
}

func (s *QualityStrategy) Name() string { return "quality" }

func (s *QualityStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	// 1. 优先从健康节点中选择质量最高的。
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

	// 2. 兜底：所有节点不健康时，仍选质量最高的。
	return selectByHighestQuality(nodes)
}
