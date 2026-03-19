package router

import (
	"log/slog"
	"sync"
	"time"

	"github.com/ai-gateway/core/internal/observability"
)

// CircuitState 定义熔断器的三种状态。
type CircuitState int

const (
	StateClosed   CircuitState = iota // 正常通信。
	StateOpen                         // 熔断开启：禁止请求。
	StateHalfOpen                     // 半开启：尝试恢复。
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

// NodeHealth 记录单个模型节点的实时运行状态。
type NodeHealth struct {
	AvgLatency          float64      // EWMA 延迟，单位秒。
	ErrorRate           float64      // 最近统计周期内的错误率 (0.0~1.0)。
	LastSuccess         time.Time    // 最后一次成功时间。
	LastFailure         time.Time    // 最后一次失败时间。
	TotalRequests       int64        // 累计请求数。
	TotalErrors         int64        // 累计错误数。
	State               CircuitState // 熔断器当前状态。
	ConsecutiveFailures int          // 连续失败次数计数器。
}

// IsHealthy 综合判定节点是否健康。
func (nh *NodeHealth) IsHealthy() bool {
	// 如果由于故障达到上限熔断，则直接判定不健康。
	if nh.State == StateOpen {
		// 检查是否到了尝试恢复的时间（例如 30 秒后进入半开状态）。
		if time.Since(nh.LastFailure) > 30*time.Second {
			return true // 允许进入半开尝试。
		}
		return false
	}

	// 传统判定策略：错误率超过 50% 且不是刚启动，视为不正常。
	if nh.TotalRequests > 5 && nh.ErrorRate > 0.5 {
		return false
	}

	return true
}

// HealthTracker 维护所有节点的实时健康状态。
type HealthTracker struct {
	mu     sync.RWMutex
	states map[string]*NodeHealth
	alpha  float64 // EWMA 衰减因子。
}

// NewHealthTracker 创建一个新的健康追踪器实例。
func NewHealthTracker(alpha float64) *HealthTracker {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.3
	}
	return &HealthTracker{
		states: make(map[string]*NodeHealth),
		alpha:  alpha,
	}
}

// RecordSuccess 记录一次成功的调用。
func (ht *HealthTracker) RecordSuccess(node string, latency time.Duration) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	h := ht.getOrCreate(node)
	h.TotalRequests++
	h.LastSuccess = time.Now()
	h.ConsecutiveFailures = 0 // 重置连续失败计数。

	// 状态转移：如果是熔断或半开状态下的成功，恢复至关闭。
	if h.State != StateClosed {
		slog.Info("熔断状态重置为闭合", "node", node, "prev_state", h.State.String())
		oldState := h.State.String()
		h.State = StateClosed
		observability.CircuitBreakerChanges.WithLabelValues(node, oldState+"->Closed").Inc()
	}

	// EWMA 更新延迟
	lat := latency.Seconds()
	if h.AvgLatency == 0 {
		h.AvgLatency = lat
	} else {
		h.AvgLatency = ht.alpha*lat + (1-ht.alpha)*h.AvgLatency
	}

	h.ErrorRate = float64(h.TotalErrors) / float64(h.TotalRequests)
}

// RecordFailure 记录一次失败的调用。
func (ht *HealthTracker) RecordFailure(node string) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	h := ht.getOrCreate(node)
	h.TotalRequests++
	h.TotalErrors++
	h.LastFailure = time.Now()
	h.ConsecutiveFailures++

	// 熔断逻辑：连续失败 5 次，则打开熔断器。
	if h.State == StateClosed && h.ConsecutiveFailures >= 5 {
		slog.Warn("连续失败达到阈值，熔断器打开", "node", node)
		h.State = StateOpen
		observability.CircuitBreakerChanges.WithLabelValues(node, "Closed->Open").Inc()
	} else if h.State == StateHalfOpen {
		// 半开状态下的任何失败都立刻回到开启状态
		slog.Warn("半开状态调用失败，重新熔断", "node", node)
		h.State = StateOpen
		observability.CircuitBreakerChanges.WithLabelValues(node, "HalfOpen->Open").Inc()
	}

	h.ErrorRate = float64(h.TotalErrors) / float64(h.TotalRequests)
}

// GetHealth 获取指定节点的健康快照。
func (ht *HealthTracker) GetHealth(node string) NodeHealth {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	if h, ok := ht.states[node]; ok {
		return *h
	}
	return NodeHealth{}
}

// IsHealthy 判定指定节点是否健康。
func (ht *HealthTracker) IsHealthy(node string) bool {
	ht.mu.RLock()
	defer ht.mu.RUnlock()

	h, ok := ht.states[node]
	if !ok {
		return true
	}
	return h.IsHealthy()
}

// getOrCreate 获取或创建节点的健康记录（调用方需持有写锁）。
func (ht *HealthTracker) getOrCreate(node string) *NodeHealth {
	h, ok := ht.states[node]
	if !ok {
		h = &NodeHealth{}
		ht.states[node] = h
	}
	return h
}
