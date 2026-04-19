package runtime

import (
	"sync"

	"github.com/ai-gateway/core/internal/observability"
)

// DependencyStatus 描述一个运行时依赖的健康状态和失败策略。
// 这些字段会同时被 /readyz、管理接口和 Prometheus 指标消费。
// DependencyStatus 描述一个运行时依赖的健康状态和失败策略。
// 这些字段会同时被 /readyz、管理接口和 Prometheus 指标消费。
type DependencyStatus struct {
	Name        string `json:"name"`          // 组件名称（如 "redis", "python-sidecar"）
	Required    bool   `json:"required"`      // 是否为核心依赖，如果不健康将导致网关整体进入 NotReady 状态
	Healthy     bool   `json:"healthy"`       // 当前连接/执行是否正常
	Status      string `json:"status"`        // 状态简述 (UP/DOWN/DEGRADED)
	Reason      string `json:"reason,omitempty"` // 状态异常的详细原因
	Version     string `json:"version,omitempty"` // 组件版本信息
	FailureMode string `json:"failure_mode,omitempty"` // 预设的失败处理模式 (fail_open/fail_closed)
}

// SystemStatus 是进程内的所有系统依赖状态注册表。
//
// 设计意图：
// 1. 真相来源：作为系统就绪检查（Readiness Check）和运维面板的唯一真相来源。
// 2. 自动化关联：每当组件状态（如 Redis 连接断开）发生变更时，自动同步更新 Prometheus 指标。
type SystemStatus struct {
	mu           sync.RWMutex
	Dependencies map[string]DependencyStatus // 内存中的依赖状态快照
}

// NewSystemStatus 初始化依赖状态表，并把网关 readiness 默认置为 true。
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

// Snapshot 返回当前所有依赖状态的只读快照副本，用于监控 API 响应。
func (s *SystemStatus) Snapshot() map[string]DependencyStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]DependencyStatus, len(s.Dependencies))
	for k, v := range s.Dependencies {
		out[k] = v
	}
	return out
}

// Ready 根据“所有必需（Required）依赖都健康”这一规则给出当前网关系统的就绪状态。
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
