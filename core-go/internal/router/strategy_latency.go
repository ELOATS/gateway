package router

import "math"

// LatencyStrategy 实现基于实时耗时的最优路径算法。
// 它动态评估各个节点的响应时延，并引入错误率惩罚机制，确保流量始终流向最快且最稳定的后端节点。
type LatencyStrategy struct {
	Tracker *HealthTracker // 必需：依靠 Tracker 提供的延迟统计数据进行实时评分
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

		// 探索机制：从未被调用过的节点给予极低的初始延迟权位。
		// 这允许新加入系统的节点或由于故障被冷落的节点能迅速获得少量的试探流量，从而触发健康状态更新。
		if health.TotalRequests == 0 {
			lat = 0.001
		} else if health.ErrorRate > 0 {
			// 负向反馈：对错误率较高的节点赋予虚拟延迟惩罚。
			// 这种逻辑可以让路由引擎在节点网络抖动或鉴权失效时，比单纯的延迟计分更敏锐地避开故障点。
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
