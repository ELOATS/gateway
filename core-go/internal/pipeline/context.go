package pipeline

import (
	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/nitro"
	"github.com/redis/go-redis/v9"
)

// DependencyContainer 封装了所有策略可能需要的共享依赖。
// 它解耦了策略工厂与具体 Pipeline 实现，提高了测试灵活性。
type DependencyContainer struct {
	IntelligenceClient pb.AiLogicClient
	NitroClient        nitro.NitroClient
	RedisClient        *redis.Client
	Config             *config.Config
}
