// Package router 提供 AI 模型节点的智能路由与多策略选择引擎。
package router

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
)

var (
	// ErrNoNodes 表示没有可用的模型节点。
	ErrNoNodes = errors.New("无可用的模型节点")

	// ErrNoStrategy 表示指定的策略不存在。
	ErrNoStrategy = errors.New("指定的路由策略不存在")
)

// SmartRouter 是路由模块的核心调度器。
// 优化为 Copy-on-Write (CoW) 机制，减少核心路径锁竞争。
type SmartRouter struct {
	mu          sync.RWMutex
	nodes       atomic.Value // 存储 []*ModelNode
	strategies  map[string]Strategy
	defaultName string
	Tracker     *HealthTracker
}

// NewSmartRouter 创建一个新的智能路由器实例。
func NewSmartRouter(nodes []*ModelNode, tracker *HealthTracker, defaultStrategy string) *SmartRouter {
	sr := &SmartRouter{
		strategies:  make(map[string]Strategy),
		defaultName: defaultStrategy,
		Tracker:     tracker,
	}
	sr.nodes.Store(nodes)
	return sr
}

// RegisterStrategy 注册一种路由策略。
func (sr *SmartRouter) RegisterStrategy(s Strategy) {
	sr.mu.Lock()
	sr.strategies[s.Name()] = s
	sr.mu.Unlock()
	slog.Info("路由策略已注册", "strategy", s.Name())
}

// Route 是路由的核心入口。
// 采用快照机制（CoW），读取节点列表无需获取锁。
func (sr *SmartRouter) Route(ctx *RouteContext) (*ModelNode, error) {
	// 快照读取。
	nodes := sr.nodes.Load().([]*ModelNode)

	// 过滤出所有已启用的节点，且排除指定的节点。
	active := sr.filterNodesSnap(nodes, ctx.ExcludeNodes)
	if len(active) == 0 {
		return nil, ErrNoNodes
	}

	sr.mu.RLock()
	strategyName := sr.resolveStrategy(ctx)
	strategy, ok := sr.strategies[strategyName]
	sr.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoStrategy, strategyName)
	}

	slog.Info("路由策略选定", "request_id", ctx.RequestID, "strategy", strategyName)

	// 执行策略选择。
	node := strategy.Select(ctx, active)

	if node == nil && strategyName != "fallback" {
		sr.mu.RLock()
		fb, exists := sr.strategies["fallback"]
		sr.mu.RUnlock()
		if exists {
			slog.Warn("首选策略无结果，回退至故障转移", "strategy", strategyName, "request_id", ctx.RequestID)
			node = fb.Select(ctx, active)
		}
	}

	if node == nil {
		slog.Warn("所有策略无结果，使用首个可用节点", "request_id", ctx.RequestID)
		node = active[0]
	}

	return node, nil
}

// UpdateNodes 动态更新模型节点列表（CoW 写入端）。
func (sr *SmartRouter) UpdateNodes(nodes []*ModelNode) {
	sr.nodes.Store(nodes)
	slog.Info("模型节点列表已更新（CoW）", "count", len(nodes))
}

// filterNodesSnap 基于快照进行过滤。
func (sr *SmartRouter) filterNodesSnap(nodes []*ModelNode, exclude []string) []*ModelNode {
	var active []*ModelNode
	excludeMap := make(map[string]bool)
	for _, e := range exclude {
		excludeMap[e] = true
	}

	for _, n := range nodes {
		if n.Enabled && !excludeMap[n.Name] {
			active = append(active, n)
		}
	}
	return active
}

// GetNodes 返回所有注册节点的副本。
func (sr *SmartRouter) GetNodes() []*ModelNode {
	nodes := sr.nodes.Load().([]*ModelNode)
	res := make([]*ModelNode, len(nodes))
	copy(res, nodes)
	return res
}

// GetStrategies 返回所有已注册策略的名称列表。
func (sr *SmartRouter) GetStrategies() []string {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	var names []string
	for name := range sr.strategies {
		names = append(names, name)
	}
	return names
}

// resolveStrategy 内部辅助，需在持有读锁时调用。
func (sr *SmartRouter) resolveStrategy(ctx *RouteContext) string {
	if hint := ctx.Header("X-Route-Strategy"); hint != "" {
		if _, ok := sr.strategies[hint]; ok {
			return hint
		}
	}
	return sr.defaultName
}
