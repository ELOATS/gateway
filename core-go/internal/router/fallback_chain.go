package router

import (
	"log/slog"
	"sync"
)

// FallbackChainManager 负责管理物理模型之间的层级降级关系。
// 设计意图：当某一特定模型（如 gpt-4）因官方 API 故障或地域性不可用时，网关能根据此链条自动将流量迁移至等效模型（如 gpt-4o 或 claude-3-5），从而确保业务不中断。
type FallbackChainManager struct {
	mu     sync.RWMutex        // 保护降级映射表的并发读写
	chains map[string][]string // 源模型到目标模型候选链的选择映射
}

func NewFallbackChainManager() *FallbackChainManager {
	return &FallbackChainManager{
		chains: make(map[string][]string),
	}
}

func (m *FallbackChainManager) SetChain(targetModel string, fallbacks []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chains[targetModel] = fallbacks
}

func (m *FallbackChainManager) GetChain(targetModel string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.chains[targetModel]
}

// GetFallbackCandidate 在降级链中按顺序寻找第一个拥有可用（Enabled 且健康）节点的模型。
// 该方法结合了静态配置与 HealthTracker 的实时评分，提供全自动的模型迁移决策。
func (m *FallbackChainManager) GetFallbackCandidate(targetModel string, nodes []*ModelNode, tracker *HealthTracker) (string, bool) {
	chain := m.GetChain(targetModel)
	if len(chain) == 0 {
		return "", false
	}

	for _, modelID := range chain {
		// 检查该模型是否有可用节点
		hasActive := false
		for _, n := range nodes {
			if n.ModelID == modelID && n.Enabled && (tracker == nil || tracker.IsHealthy(n.Name)) {
				hasActive = true
				break
			}
		}
		if hasActive {
			slog.Info("Model fallback triggered", "from", targetModel, "to", modelID)
			return modelID, true
		}
	}

	return "", false
}
