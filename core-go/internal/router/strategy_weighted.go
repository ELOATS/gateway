package router

import "time"

// WeightedStrategy 基于权重随机分配流量，适用于 A/B 测试与灰度发布。
// 权重为 80:20 意味着约 80% 的请求流向高权重节点。
// 在 P1 阶段增强：它现在会利用 HealthTracker 过滤掉不健康的节点。
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

	// 1. 优先过滤出健康节点。
	for _, n := range nodes {
		if s.Tracker == nil || s.Tracker.IsHealthy(n.Name) {
			healthyNodes = append(healthyNodes, n)
			total += n.Weight
		}
	}

	// 2. 熔断保护：若当前所有候选节点都不健康，降级使用全部节点以保证服务可用。
	if len(healthyNodes) == 0 {
		healthyNodes = nodes
		for _, n := range nodes {
			total += n.Weight
		}
	}

	if total <= 0 {
		return healthyNodes[0]
	}

	// 3. 基于纳秒时间戳的伪随机选择。
	pick := time.Now().UnixNano() % int64(total)
	var acc int64
	for _, n := range healthyNodes {
		acc += int64(n.Weight)
		if pick < acc {
			return n
		}
	}

	return healthyNodes[0]
}
