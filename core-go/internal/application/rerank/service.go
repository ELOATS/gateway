package rerank

import (
	"context"
	"fmt"

	"github.com/ai-gateway/core/internal/adapters"
	"github.com/ai-gateway/core/pkg/models"
)

// Service 负责处理 Rerank 业务逻辑。
type Service struct {
	providers map[string]adapters.RerankProvider
}

func NewService(providers map[string]adapters.RerankProvider) *Service {
	return &Service{
		providers: providers,
	}
}

// Rerank 执行文档重排序。
func (s *Service) Rerank(ctx context.Context, req *models.RerankRequest) (*models.RerankResponse, error) {
	provider, ok := s.providers[req.Model]
	if !ok {
		// 如果没找到精确匹配，尝试使用默认或者按前缀匹配（如 cohere-）
		// 这里暂且要求显式配置
		return nil, fmt.Errorf("no rerank provider found for model: %s", req.Model)
	}

	return provider.Rerank(ctx, req)
}
