package router

import "math/rand/v2"

// WeightedStrategy 按节点权重分配流量，适合灰度发布和 A/B 实验。
// 如果配置了 HealthTracker，会优先过滤不健康节点。
type WeightedStrategy struct {
	Tracker *HealthTracker
}

func (s *WeightedStrategy) Name() string { return "weighted" }

func (s *WeightedStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	var total int
	var healthyNodes []*ModelNode

	for _, n := range nodes {
		if s.Tracker == nil || s.Tracker.IsHealthy(n.Name) {
			healthyNodes = append(healthyNodes, n)
			total += n.Weight
		}
	}

	// 如果全部节点都不健康，则退化到全量候选，尽量保持服务可用。
	if len(healthyNodes) == 0 {
		healthyNodes = nodes
		total = 0
		for _, n := range nodes {
			total += n.Weight
		}
	}

	if total <= 0 {
		return healthyNodes[0]
	}

	pick := int64(rand.IntN(total))
	var acc int64
	for _, n := range healthyNodes {
		acc += int64(n.Weight)
		if pick < acc {
			return n
		}
	}

	return healthyNodes[0]
}
