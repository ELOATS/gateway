package router

import (
	"log/slog"
	"time"
)

// FallbackStrategy 实现级联故障转移。
// 按节点列表顺序，跳过所有不健康的节点，返回第一个健康节点。
// 如果所有节点均不健康，则选择最近一次成功调用最新的节点（"死马当活马医"策略）。
type FallbackStrategy struct {
	Tracker *HealthTracker
}

func (s *FallbackStrategy) Name() string { return "fallback" }

func (s *FallbackStrategy) Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode {
	if len(nodes) == 0 {
		return nil
	}

	// 1. 按顺序找到第一个健康节点。
	for _, n := range nodes {
		if s.Tracker.IsHealthy(n.Name) {
			return n
		}
	}

	// 2. 所有节点不健康：选择"最近成功"时间最近的节点，
	// 赌它可能已经恢复。
	slog.Warn("所有节点不健康，启用兜底策略", "request_id", ctx.RequestID)

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
