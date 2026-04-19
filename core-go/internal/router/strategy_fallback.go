package router

import (
	"log/slog"
	"time"
)

// FallbackStrategy 实现灾备回退策略。
// 它是系统的高可用防线：优先返回当前存活的节点；若所有节点在健康监测中均判定为“不健康”，
// 则会寻找“最后一次调用成功时间”最接近现在的节点。
type FallbackStrategy struct {
	Tracker *HealthTracker // 必需：依靠 Tracker 提供的历史调用时间戳快照
}

func (s *FallbackStrategy) Name() string { return "fallback" }

func (s *FallbackStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	for _, n := range nodes {
		if s.Tracker.IsHealthy(n.Name) {
			return n
		}
	}

	slog.Warn("所有候选节点均不健康，启用回退策略", "request_id", ctx.RequestID)

	// 灾难级降级逻辑：
	// 如果没有任何节点是健康的，我们不能直接拒绝对话请求。此时根据“玄学”经验，
	// 前一刻还成功的节点，即便现在判定为不健康（可能是因为网络抖动），其恢复的可能性通常大于长时间处于故障状态的节点。
	var bestNode *ModelNode
	var bestTime time.Time
	for _, n := range nodes {
		h := s.Tracker.GetHealth(n.Name)
		if bestNode == nil || h.LastSuccess.After(bestTime) {
			bestNode = n
			bestTime = h.LastSuccess
		}
	}

	return bestNode
}
