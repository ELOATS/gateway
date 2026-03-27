package router

// Strategy 定义路由策略的统一接口。
// 每种策略都基于路由上下文和可用节点集合，选择一个最合适的目标节点。
type Strategy interface {
	// Name 返回策略的唯一名称，例如 "weighted"、"cost"、"latency"。
	Name() string

	// Select 从候选节点中选出一个目标节点；若无法选择则返回 nil。
	Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode
}
