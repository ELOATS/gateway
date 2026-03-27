package router

// RouteContext 汇总路由决策所需的请求级信息。
// HTTP 层会把原始请求压缩成这些特征，供策略统一评估。
type RouteContext struct {
	RequestID    string            // 请求追踪 ID。
	Model        string            // 客户端请求的模型名称。
	PromptTokens int               // 预估的输入 Token 数。
	UserTier     string            // 用户等级，例如 admin、free。
	Headers      map[string]string // 可用于路由提示的请求头。
	ExcludeNodes []string          // 本轮显式排除的节点。
}

// Header 安全读取请求头，避免调用方直接处理 nil map。
func (rc *RouteContext) Header(key string) string {
	if rc.Headers == nil {
		return ""
	}
	return rc.Headers[key]
}
