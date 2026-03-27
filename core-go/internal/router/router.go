package router

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
)

var (
	// ErrNoNodes 表示当前没有可用于路由的节点。
	ErrNoNodes = errors.New("无可用的模型节点")

	// ErrNoStrategy 表示请求指定了一个不存在的路由策略。
	ErrNoStrategy = errors.New("指定的路由策略不存在")
)

// SmartRouter 是模型路由模块的核心调度器。
// 节点列表使用 Copy-on-Write 快照存储，读路径不需要拿锁，减少主链路竞争。
type SmartRouter struct {
	mu          sync.RWMutex
	nodes       atomic.Value // 存储 []*ModelNode 的只读快照
	strategies  map[string]Strategy
	defaultName string
	Tracker     *HealthTracker
}

// NewSmartRouter 创建一个新的路由器实例。
func NewSmartRouter(nodes []*ModelNode, tracker *HealthTracker, defaultStrategy string) *SmartRouter {
	sr := &SmartRouter{
		strategies:  make(map[string]Strategy),
		defaultName: defaultStrategy,
		Tracker:     tracker,
	}
	sr.nodes.Store(nodes)
	return sr
}

// RegisterStrategy 注册一个可供请求选择的路由策略。
func (sr *SmartRouter) RegisterStrategy(s Strategy) {
	sr.mu.Lock()
	sr.strategies[s.Name()] = s
	sr.mu.Unlock()
	slog.Info("路由策略已注册", "strategy", s.Name())
}

// Route 是统一的路由入口。
// 它会先过滤可用节点，再按请求上下文选择策略；如果主策略没有选出节点，则退回 fallback。
func (sr *SmartRouter) Route(ctx *RouteContext) (*ModelNode, error) {
	nodes := sr.nodes.Load().([]*ModelNode)
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
	node := strategy.Select(ctx, active)

	if node == nil && strategyName != "fallback" {
		sr.mu.RLock()
		fallback, exists := sr.strategies["fallback"]
		sr.mu.RUnlock()
		if exists {
			slog.Warn("主策略无结果，回退到 fallback", "request_id", ctx.RequestID, "strategy", strategyName)
			node = fallback.Select(ctx, active)
		}
	}

	if node == nil {
		slog.Warn("所有策略都未选出节点，使用第一个可用节点兜底", "request_id", ctx.RequestID)
		node = active[0]
	}

	return node, nil
}

// UpdateNodes 使用 CoW 方式替换整个节点快照。
func (sr *SmartRouter) UpdateNodes(nodes []*ModelNode) {
	sr.nodes.Store(nodes)
	slog.Info("模型节点列表已更新", "count", len(nodes))
}

// filterNodesSnap 在只读快照上执行过滤，不修改原始节点列表。
func (sr *SmartRouter) filterNodesSnap(nodes []*ModelNode, exclude []string) []*ModelNode {
	var active []*ModelNode
	excludeMap := make(map[string]bool)
	for _, name := range exclude {
		excludeMap[name] = true
	}

	for _, n := range nodes {
		if n.Enabled && !excludeMap[n.Name] {
			active = append(active, n)
		}
	}
	return active
}

// GetNodes 返回节点快照的副本，避免调用方直接修改内部切片。
func (sr *SmartRouter) GetNodes() []*ModelNode {
	nodes := sr.nodes.Load().([]*ModelNode)
	res := make([]*ModelNode, len(nodes))
	copy(res, nodes)
	return res
}

// GetStrategies 返回当前已注册的策略名列表。
func (sr *SmartRouter) GetStrategies() []string {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	var names []string
	for name := range sr.strategies {
		names = append(names, name)
	}
	return names
}

// resolveStrategy 根据请求头中的 hint 或默认值选择最终策略名。
func (sr *SmartRouter) resolveStrategy(ctx *RouteContext) string {
	if hint := ctx.Header("X-Route-Strategy"); hint != "" {
		if _, ok := sr.strategies[hint]; ok {
			return hint
		}
	}
	return sr.defaultName
}
