package runtime

import (
	"sync"

	"github.com/ai-gateway/core/internal/observability"
)

// DependencyStatus 描述一个运行时依赖的健康状态和失败策略。
// 这些字段会同时被 /readyz、管理接口和 Prometheus 指标消费。
type DependencyStatus struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Healthy     bool   `json:"healthy"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Version     string `json:"version,omitempty"`
	FailureMode string `json:"failure_mode,omitempty"`
}

// SystemStatus 是进程内的依赖状态表。
// 它既承担 readiness 判断，也负责把状态变更同步到观测指标。
type SystemStatus struct {
	mu           sync.RWMutex
	Dependencies map[string]DependencyStatus
}

// NewSystemStatus 初始化依赖状态表，并把网关 readiness 默认置为 1。
// 后续随着依赖注册，会根据 required + healthy 的组合重新计算 readiness。
func NewSystemStatus() *SystemStatus {
	s := &SystemStatus{
		Dependencies: make(map[string]DependencyStatus),
	}
	observability.RecordGatewayReadiness(true)
	return s
}

// Set 会覆盖指定依赖的当前状态，并同步刷新对应的 Prometheus 指标。
func (s *SystemStatus) Set(dep DependencyStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.Dependencies[dep.Name]; ok {
		observability.RemoveDependencyStatus(prev.Name, prev.Status, prev.FailureMode, prev.Version)
	}
	s.Dependencies[dep.Name] = dep
	observability.RecordDependencyStatus(dep.Name, dep.Status, dep.FailureMode, dep.Version, dep.Required, dep.Healthy)
	observability.RecordGatewayReadiness(s.readyLocked())
}

// Snapshot 返回一份只读快照，供 readiness、管理接口和 dashboard 使用。
func (s *SystemStatus) Snapshot() map[string]DependencyStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]DependencyStatus, len(s.Dependencies))
	for k, v := range s.Dependencies {
		out[k] = v
	}
	return out
}

// Ready 根据“所有必需依赖都健康”这一规则给出当前 readiness。
func (s *SystemStatus) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readyLocked()
}

func (s *SystemStatus) readyLocked() bool {
	for _, dep := range s.Dependencies {
		if dep.Required && !dep.Healthy {
			return false
		}
	}
	return true
}
