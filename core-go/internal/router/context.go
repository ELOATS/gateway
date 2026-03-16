package router

// RouteContext 包含路由决策所需的请求级信息。
// 它从 HTTP 请求中提取关键特征，供策略引擎做出最优决策。
type RouteContext struct {
	RequestID    string            // 请求追踪 ID。
	Model        string            // 客户端请求的模型名称。
	PromptTokens int               // 预估的 Prompt Token 数量。
	UserTier     string            // 用户等级（从 API Key Label 提取，如 "admin", "free"）。
	Headers      map[string]string // 请求头中的路由暗示（如 X-Route-Strategy）。
	ExcludeNodes []string          // 需要排除的节点列表。
}

// Header 安全地从 Headers 中读取请求头值。
func (rc *RouteContext) Header(key string) string {
	if rc.Headers == nil {
		return ""
	}
	return rc.Headers[key]
}
