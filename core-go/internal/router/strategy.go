package router

// Strategy 定义路由策略的统一接口。
// 每种策略根据路由上下文和可用节点集，选择最佳的目标节点。
// 若无法选择，返回 nil。
type Strategy interface {
	// Name 返回策略的唯一标识名称（如 "weighted", "cost", "latency"）。
	Name() string

	// Select 从候选节点中选择最优的目标节点。
	// nodes 保证非空且所有节点均已启用。
	Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode
}
