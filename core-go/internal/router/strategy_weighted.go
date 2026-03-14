package router

import "time"

// WeightedStrategy 基于权重随机分配流量，适用于 A/B 测试与灰度发布。
// 权重为 80:20 意味着约 80% 的请求流向高权重节点。
type WeightedStrategy struct{}

func (s *WeightedStrategy) Name() string { return "weighted" }

func (s *WeightedStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	var total int
	for _, n := range nodes {
		total += n.Weight
	}
	if total <= 0 {
		return nodes[0]
	}

	// 基于纳秒时间戳的伪随机选择（轻量级，无需 math/rand）。
	pick := time.Now().UnixNano() % int64(total)
	var acc int64
	for _, n := range nodes {
		acc += int64(n.Weight)
		if pick < acc {
			return n
		}
	}

	return nodes[0]
}
