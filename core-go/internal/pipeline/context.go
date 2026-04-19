package pipeline

import (
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/db"
	"github.com/ai-gateway/core/internal/dependencies"
	"github.com/redis/go-redis/v9"
)

// DependencyContainer 封装了策略引擎初始化所需的共享依赖。
// 它作为一个组件容器，将数据库、配置、缓存等基础设施注入到各个策略实例中。
type DependencyContainer struct {
	Dependencies  *dependencies.Facade // 后端服务门面（安全、计数等）
	RedisClient   *redis.Client        // 分布式状态存储（限流、配额）
	Config        *config.Config       // 全局静态配置
	TenantManager db.TenantManager     // 租户信息存取
	CostEngine    db.CostEngine        // 计费与审计
}

// RequestMetadata 存放从传输层（HTTP/gRPC）提取的元数据。
// 它解耦了具体的 Web 框架（如 Gin），使得 Pipeline 逻辑可以在不同协议层复用。
type RequestMetadata struct {
	Headers  map[string]string // 原始请求头副本
	TenantID uint              // 解析出的租户 ID
	APIKeyID uint              // 解析出的 API Key ID
}
