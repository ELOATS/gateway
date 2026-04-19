package router

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
)

var (
	// ErrNoNodes 表示当前请求的模型 ID 在所有可用节点中均未找到匹配项。
	ErrNoNodes = errors.New("无可用的模型节点")

	// ErrNoStrategy 表示请求指定的路由策略（通过 Header 传入）在系统中未定义。
	ErrNoStrategy = errors.New("指定的路由策略不存在")
)

// SmartRouter 是模型路由模块的核心调度器。
// 它负责根据不同的策略（权重、质量、延迟等）将请求分发到最合适的后端 ModelNode。
// 架构设计：
// 1. 并发安全：节点列表使用 atomic.Value 进行 Copy-on-Write (CoW) 存储，读操作无需加锁，适合高频访问的主链路。
// 2. 灾备能力：集成 HealthTracker 进行健康监测，并支持二级 Model-level Fallback。
type SmartRouter struct {
	mu          sync.RWMutex          // 保护策略映射表的修改
	nodes       atomic.Value          // 存储 []*ModelNode 的只读快照，加速并发读取
	strategies  map[string]Strategy   // 已注册的可选路由策略集合
	defaultName     string            // 无特殊需求时的默认策略名
	Tracker         *HealthTracker    // 健康状态追踪计分器
	FallbackManager *FallbackChainManager // 模型降级关系链管理器
}

// NewSmartRouter 创建一个新的路由器实例。
func NewSmartRouter(nodes []*ModelNode, tracker *HealthTracker, defaultStrategy string) *SmartRouter {
	sr := &SmartRouter{
		strategies:      make(map[string]Strategy),
		defaultName:     defaultStrategy,
		Tracker:         tracker,
		FallbackManager: NewFallbackChainManager(),
	}
	sr.nodes.Store(nodes)
	return sr
}

// RegisterStrategy 将一个路由策略实例注册到路由器中，使其可供客户端按需调用。
func (sr *SmartRouter) RegisterStrategy(s Strategy) {
	sr.mu.Lock()
	sr.strategies[s.Name()] = s
	sr.mu.Unlock()
	slog.Info("Route strategy registered", "strategy", s.Name())
}

// Route 是整个网关获取执行节点的唯一入口。
// 执行流程：
// 1. 过滤：基于当前 CoW 快照筛选满足（启用、匹配模型、不在禁选列表）的物理节点。
// 2. 模型降级：若主模型节点全部宕机，尝试根据降级链寻找备选模型（如 gpt-4 故障降级到 gpt-3.5）。
// 3. 策略选择：基于上下文或 Header 选择路由策略进行最终节点择优。
func (sr *SmartRouter) Route(ctx *RouteContext) (*ModelNode, error) {
	nodes := sr.nodes.Load().([]*ModelNode)
	active := sr.filterNodesSnap(nodes, ctx.Model, ctx.ExcludeNodes)

	// 如果主请求模型没有可用节点，触发二级模型级降级（Model-level Fallback）
	if len(active) == 0 && sr.FallbackManager != nil {
		if fallbackModel, ok := sr.FallbackManager.GetFallbackCandidate(ctx.Model, nodes, sr.Tracker); ok {
			slog.Warn("Attempting model-level fallback", "request_id", ctx.RequestID, "old_model", ctx.Model, "new_model", fallbackModel)
			ctx.Model = fallbackModel
			active = sr.filterNodesSnap(nodes, ctx.Model, ctx.ExcludeNodes)
		}
	}

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

	slog.Info("Route strategy selected", "request_id", ctx.RequestID, "strategy", strategyName)
	node := strategy.Select(ctx, active)

	// 如果主选策略（如策略指定的后端响应超时或熔断）未返回节点，退而求其次使用基础的故障转移策略
	if node == nil && strategyName != "fallback" {
		sr.mu.RLock()
		fallback, exists := sr.strategies["fallback"]
		sr.mu.RUnlock()
		if exists {
			slog.Warn("Main strategy yielded no result, reverting to fallback", "request_id", ctx.RequestID, "strategy", strategyName)
			node = fallback.Select(ctx, active)
		}
	}

	// 兜底：如果所有策略均失效，选择第一个存活节点强行执行，保证可用性优于报错
	if node == nil {
		slog.Warn("No strategy returned a node, falling back to first available", "request_id", ctx.RequestID)
		node = active[0]
	}

	return node, nil
}

// UpdateNodes 使用 CoW 方式替换整个节点快照。
func (sr *SmartRouter) UpdateNodes(nodes []*ModelNode) {
	sr.nodes.Store(nodes)
	slog.Info("Model nodes updated", "count", len(nodes))
}

// filterNodesSnap 在只读快照上执行过滤，不修改原始节点列表。
func (sr *SmartRouter) filterNodesSnap(nodes []*ModelNode, modelID string, exclude []string) []*ModelNode {
	var active []*ModelNode
	excludeMap := make(map[string]bool)
	for _, name := range exclude {
		excludeMap[name] = true
	}

	for _, n := range nodes {
		// 必须满足：启用、不被排除、且模型 ID 匹配
		if n.Enabled && !excludeMap[n.Name] && n.ModelID == modelID {
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

// resolveStrategy 分析请求上下文，决定最终使用的路由算法名称。
// 只有高级用户（Admin/Premium）可通过 Header 指定特定策略；普通用户始终遵循系统默认策略。
func (sr *SmartRouter) resolveStrategy(ctx *RouteContext) string {
	if hint := ctx.Header("X-Route-Strategy"); hint != "" {
		if ctx.UserTier == "admin" || ctx.UserTier == "premium" {
			if _, ok := sr.strategies[hint]; ok {
				return hint
			}
		}
	}
	return sr.defaultName
}
