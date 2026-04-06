package pipeline

import (
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/dependencies"
	"github.com/redis/go-redis/v9"
)

// DependencyContainer 封装了策略引擎初始化所需的共享依赖。
type DependencyContainer struct {
	Dependencies *dependencies.Facade
	RedisClient  *redis.Client
	Config       *config.Config
}
