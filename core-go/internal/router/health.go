package router

import (
	"log/slog"
	"sync"
	"time"

	"github.com/ai-gateway/core/internal/observability"
)

// CircuitState 描述节点熔断器的运行状态。
type CircuitState int

const (
	StateClosed   CircuitState = iota // 正常通信
	StateOpen                         // 熔断打开，暂时拒绝流量
	StateHalfOpen                     // 半开，允许少量流量验证恢复情况
)

func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "Closed"
	case StateOpen:
		return "Open"
	case StateHalfOpen:
		return "HalfOpen"
	default:
		return "Unknown"
	}
}

// NodeHealth 记录单个模型节点的实时健康快照。
type NodeHealth struct {
	AvgLatency          float64
	ErrorRate           float64
	LastSuccess         time.Time
	LastFailure         time.Time
	TotalRequests       int64
	TotalErrors         int64
	State               CircuitState
	ConsecutiveFailures int
}

// IsHealthy 根据熔断状态和最近错误率综合判断节点是否健康。
func (nh *NodeHealth) IsHealthy() bool {
	if nh.State == StateOpen {
		// 打开状态下只在冷却时间过去后允许进入半开探测。
		if time.Since(nh.LastFailure) > 30*time.Second {
			return true
		}
		return false
	}

	if nh.TotalRequests > 5 && nh.ErrorRate > 0.5 {
		return false
	}
	return true
}

// HealthTracker 维护所有节点的健康状态，并支持 EWMA 延迟和简单熔断逻辑。
type HealthTracker struct {
	mu     sync.RWMutex
	states map[string]*NodeHealth
	alpha  float64
}

// NewHealthTracker 创建健康追踪器并开启自愈背景协程。
func NewHealthTracker(alpha float64) *HealthTracker {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.3
	}
	ht := &HealthTracker{
		states: make(map[string]*NodeHealth),
		alpha:  alpha,
	}
	go ht.proactiveSelfHealing()
	return ht
}

// proactiveSelfHealing 定期扫描熔断节点并尝试恢复。
func (ht *HealthTracker) proactiveSelfHealing() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		ht.mu.Lock()
		for node, h := range ht.states {
			if h.State == StateOpen {
				// 达到冷却时间后，自动进入半开状态进行探测
				if time.Since(h.LastFailure) > 30*time.Second {
					oldState := h.State.String()
					h.State = StateHalfOpen
					h.ConsecutiveFailures = 0 // 重置计数，给一次机会
					slog.Info("节点进入半开探测状态", "node", node, "prev_state", oldState)
					observability.CircuitBreakerChanges.WithLabelValues(node, oldState+"->HalfOpen").Inc()
				}
			}
		}
		ht.mu.Unlock()
	}
}

// RecordSuccess 记录一次成功调用，并更新延迟、错误率和熔断状态。
func (ht *HealthTracker) RecordSuccess(node string, latency time.Duration) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	h := ht.getOrCreate(node)
	h.TotalRequests++
	h.LastSuccess = time.Now()
	h.ConsecutiveFailures = 0

	if h.State != StateClosed {
		oldState := h.State.String()
		h.State = StateClosed
		slog.Info("节点熔断状态恢复为 Closed", "node", node, "prev_state", oldState)
		observability.CircuitBreakerChanges.WithLabelValues(node, oldState+"->Closed").Inc()
	}

	latencySeconds := latency.Seconds()
	if h.AvgLatency == 0 {
		h.AvgLatency = latencySeconds
	} else {
		h.AvgLatency = ht.alpha*latencySeconds + (1-ht.alpha)*h.AvgLatency
	}

	h.ErrorRate = float64(h.TotalErrors) / float64(h.TotalRequests)
}

// RecordFailure 记录一次失败调用，并在连续失败达到阈值后打开熔断器。
func (ht *HealthTracker) RecordFailure(node string) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	h := ht.getOrCreate(node)
	h.TotalRequests++
	h.TotalErrors++
	h.LastFailure = time.Now()
	h.ConsecutiveFailures++

	if h.State == StateClosed && h.ConsecutiveFailures >= 5 {
		h.State = StateOpen
		slog.Warn("连续失败达到阈值，打开熔断器", "node", node)
		observability.CircuitBreakerChanges.WithLabelValues(node, "Closed->Open").Inc()
	} else if h.State == StateHalfOpen {
		h.State = StateOpen
		slog.Warn("半开状态探测失败，重新打开熔断器", "node", node)
		observability.CircuitBreakerChanges.WithLabelValues(node, "HalfOpen->Open").Inc()
	}

	h.ErrorRate = float64(h.TotalErrors) / float64(h.TotalRequests)
}

// GetHealth 返回指定节点的健康快照。
func (ht *HealthTracker) GetHealth(node string) NodeHealth {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	if h, ok := ht.states[node]; ok {
		return *h
	}
	return NodeHealth{}
}

// IsHealthy 判断指定节点当前是否健康。
func (ht *HealthTracker) IsHealthy(node string) bool {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	h, ok := ht.states[node]
	if !ok {
		return true
	}
	return h.IsHealthy()
}

// getOrCreate 在调用方持有写锁的前提下获取或初始化节点状态。
func (ht *HealthTracker) getOrCreate(node string) *NodeHealth {
	h, ok := ht.states[node]
	if !ok {
		h = &NodeHealth{}
		ht.states[node] = h
	}
	return h
}
