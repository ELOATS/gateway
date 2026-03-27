package router

import "math"

// LatencyStrategy 根据实时延迟和错误率优先选择响应更快的节点。
// 当所有节点都不健康时，仍会返回“估计延迟最低”的节点作为尽力而为的兜底。
type LatencyStrategy struct {
	Tracker *HealthTracker
}

func (s *LatencyStrategy) Name() string { return "latency" }

func (s *LatencyStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	var bestHealthy *ModelNode
	bestHealthyLatency := math.MaxFloat64

	var bestAny *ModelNode
	bestAnyLatency := math.MaxFloat64

	for _, n := range nodes {
		health := s.Tracker.GetHealth(n.Name)
		lat := health.AvgLatency

		// 从未调用过的节点给一个很低的虚拟延迟，便于新节点获得少量探索流量。
		if health.TotalRequests == 0 {
			lat = 0.001
		} else if health.ErrorRate > 0 {
			// 对错误率高的节点附加惩罚，让负反馈更快反映到路由结果。
			lat = lat * (1.0 + health.ErrorRate*5.0)
		}

		if lat < bestAnyLatency {
			bestAnyLatency = lat
			bestAny = n
		}

		if health.IsHealthy() && lat < bestHealthyLatency {
			bestHealthyLatency = lat
			bestHealthy = n
		}
	}

	if bestHealthy != nil {
		return bestHealthy
	}
	return bestAny
}
