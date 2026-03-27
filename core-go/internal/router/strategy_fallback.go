package router

import (
	"log/slog"
	"time"
)

// FallbackStrategy 负责在健康节点不足时做保守降级。
// 它会优先返回当前健康节点；若全部不健康，则挑选最近一次成功调用最新的节点。
type FallbackStrategy struct {
	Tracker *HealthTracker
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
