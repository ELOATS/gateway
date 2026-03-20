package router

import "math"

// LatencyStrategy 基于 HealthTracker 的实时 EWMA 延迟统计，选择当前响应最快的健康节点。
// 仅考虑健康节点；若所有节点均不健康，则选择延迟最低的节点（尽力而为）。
type LatencyStrategy struct {
	Tracker *HealthTracker
}

func (s *LatencyStrategy) Name() string { return "latency" }

func (s *LatencyStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	// 1. 优先从健康节点中选择延迟最低的。
	var bestHealthy *ModelNode
	bestHealthyLatency := math.MaxFloat64

	// 2. 同时追踪全局延迟最低的节点（用于兜底）。
	var bestAny *ModelNode
	bestAnyLatency := math.MaxFloat64

	for _, n := range nodes {
		health := s.Tracker.GetHealth(n.Name)
		lat := health.AvgLatency

		// 从未被调用过的节点赋予极低延迟以优先尝试（探索机制）。
		if health.TotalRequests == 0 {
			lat = 0.001
		} else if health.ErrorRate > 0 {
			// 引入基于实时错误率的延迟惩罚：错误率越高，虚拟计算延迟呈指数或常数级放大，减少该节点的流量分配。
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
	return bestAny // 兜底：所有节点不健康时选延迟最低的。
}
