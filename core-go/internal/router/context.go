package router

// RouteContext 汇总了路由决策所需的请求级特征信息。
// 它的设计目的是解耦具体的协议层（如 HTTP 或 gRPC），将请求特征压缩为标准字段供路由器策略评估。
type RouteContext struct {
	RequestID    string            // 全链路追踪 ID
	Model        string            // 客户端最初请求的目标模型标识符
	PromptTokens int               // 预估或精确统计的输入 Token 总量
	UserTier     string            // 用户层级标签（用于路由优先级划分）
	Headers      map[string]string // 路由相关的元数据（如 X-Route-Strategy 提示）
	ExcludeNodes []string          // 显式排除列表（通常用于重试时避开已故障节点）
}

// Header 安全地获取上下文中缓存的指定请求头信息。
func (rc *RouteContext) Header(key string) string {
	if rc.Headers == nil {
		return ""
	}
	return rc.Headers[key]
}
