package router

// Strategy 定义了物理节点路由算法的通用接口。
// 每种具体的策略（如加权轮询、延迟优先等）都根据请求上下文和健康快照从中择优。
type Strategy interface {
	// Name 返回策略的唯一标识名称（用于从 Header 或配置中映射）。
	Name() string

	// Select 根据算法逻辑从候选节点集中选出一个最优节点。
	// 若无可用节点或算法无法收敛，应返回 nil。
	Select(ctx *RouteContext, nodes []*ModelNode) *ModelNode
}
