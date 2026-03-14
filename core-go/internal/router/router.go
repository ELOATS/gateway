// Package router 提供 AI 模型节点的智能路由与多策略选择引擎。
package router

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

var (
	// ErrNoNodes 表示没有可用的模型节点。
	ErrNoNodes = errors.New("无可用的模型节点")

	// ErrNoStrategy 表示指定的策略不存在。
	ErrNoStrategy = errors.New("指定的路由策略不存在")
)

// SmartRouter 是路由模块的核心调度器。
// 它持有模型注册表、健康追踪器和策略池，负责为每个请求选择最优节点。
type SmartRouter struct {
	mu          sync.RWMutex
	nodes       []*ModelNode
	strategies  map[string]Strategy
	defaultName string
	Tracker     *HealthTracker
}

// NewSmartRouter 创建一个新的智能路由器实例。
func NewSmartRouter(nodes []*ModelNode, tracker *HealthTracker, defaultStrategy string) *SmartRouter {
	return &SmartRouter{
		nodes:       nodes,
		strategies:  make(map[string]Strategy),
		defaultName: defaultStrategy,
		Tracker:     tracker,
	}
}

// RegisterStrategy 注册一种路由策略。
func (sr *SmartRouter) RegisterStrategy(s Strategy) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.strategies[s.Name()] = s
	slog.Info("路由策略已注册", "strategy", s.Name())
}

// Route 是路由的核心入口。
// 策略选择优先级：
//  1. 请求头 X-Route-Strategy 显式指定
//  2. 以上无指定 → 使用默认策略
//
// 如果首选策略返回 nil，自动回退到 fallback 策略。
func (sr *SmartRouter) Route(ctx *RouteContext) (*ModelNode, error) {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	// 过滤出所有已启用的节点。
	active := sr.activeNodes()
	if len(active) == 0 {
		return nil, ErrNoNodes
	}

	// 确定使用的策略。
	strategyName := sr.resolveStrategy(ctx)
	strategy, ok := sr.strategies[strategyName]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoStrategy, strategyName)
	}

	slog.Info("路由策略选定", "request_id", ctx.RequestID, "strategy", strategyName)

	// 执行策略选择。
	node := strategy.Select(ctx, active)

	// 若首选策略未返回结果，回退到 fallback。
	if node == nil && strategyName != "fallback" {
		if fb, exists := sr.strategies["fallback"]; exists {
			slog.Warn("首选策略无结果，回退至故障转移", "strategy", strategyName, "request_id", ctx.RequestID)
			node = fb.Select(ctx, active)
		}
	}

	// 最终兜底：策略均无结果，选第一个节点。
	if node == nil {
		slog.Warn("所有策略无结果，使用首个可用节点", "request_id", ctx.RequestID)
		node = active[0]
	}

	return node, nil
}

// UpdateNodes 以线程安全的方式动态更新模型节点列表。
func (sr *SmartRouter) UpdateNodes(nodes []*ModelNode) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.nodes = nodes
}

// activeNodes 返回所有已启用的节点。
func (sr *SmartRouter) activeNodes() []*ModelNode {
	var active []*ModelNode
	for _, n := range sr.nodes {
		if n.Enabled {
			active = append(active, n)
		}
	}
	return active
}

// resolveStrategy 确定当前请求应使用的策略名称。
func (sr *SmartRouter) resolveStrategy(ctx *RouteContext) string {
	// 优先级 1：请求头显式指定。
	if hint := ctx.Header("X-Route-Strategy"); hint != "" {
		if _, ok := sr.strategies[hint]; ok {
			return hint
		}
		slog.Warn("请求头指定的策略不存在，回退到默认", "hint", hint)
	}

	// 优先级 2：默认策略。
	return sr.defaultName
}
