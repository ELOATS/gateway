package router

import "math/rand/v2"

// WeightedStrategy 实现加权随机路由逻辑。
// 它非常适合用于 A/B 测试或灰度发布，通过配置不同的 Weight 值来控制流量配比。
// 此外，它集成了 HealthTracker，会自动剔除由于网络或认证问题导致不健康的节点。
type WeightedStrategy struct {
	Tracker *HealthTracker // 可选的健康状态追踪器
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

	// 容灾处理：如果所有节点被 HealthTracker 标记为故障。
	// 为了保证服务的最大可用性（Best-effort），我们会退化到使用全量候选节点，防止因追踪器误判导致完全无法服务。
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
