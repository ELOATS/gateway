package router

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ai-gateway/core/internal/observability"
)

// CircuitState 描述节点熔断器的当前运行状态。
type CircuitState int

const (
	StateClosed   CircuitState = iota // 正常通信状态：允许所有流量通过
	StateOpen                         // 熔断打开状态：后端故障，暂时拦截该节点的所有请求
	StateHalfOpen                     // 半开探测状态：允许极少量流量尝试访问，以验证后端是否已修复
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

// NodeHealth 记录单个物理模型节点的实时健康画像与性能指标。
type NodeHealth struct {
	AvgLatency          float64      // 指数加权移动平均（EWMA）计算出的实时延迟，更敏锐地反映波动
	ErrorRate           float64      // 窗口期内的历史错误率
	LastSuccess         time.Time    // 最后一次执行成功的精准时间戳
	LastFailure         time.Time    // 最后一次执行失败的时间戳（用于计算冷却期）
	TotalRequests       int64        // 总请求计数
	TotalErrors         int64        // 总错误计数
	State               CircuitState // 当前熔断器状态
	ConsecutiveFailures int          // 连续失败次数，用于触发熔断
}

// IsHealthy 综合评估节点当前的可用性。
// 如果节点处于熔断打开状态，但在“观察冷却期”（30s）之后仍未成功，则暂时保持不健康。
// 此外，如果样本数量充足（>5）且错误率超过 50%，也会被标记为不健康状态。
func (nh *NodeHealth) IsHealthy() bool {
	if nh.State == StateOpen {
		// 探测机制：只有在冷却时间过去后，路由逻辑才会给该节点一个“假健康”的假象，从而引导请求进入半开探测
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

// HealthTracker 负责跨请求维护所有模型节点的健康画像。
// 它采用 EWMA 算法平滑延迟波动，并管理一套简单的熔断与自愈状态机。
type HealthTracker struct {
	mu     sync.RWMutex           // 读写锁，保护状态映射表及其指标更新
	states map[string]*NodeHealth // 节点名称到健康画像的映射
	alpha  float64                // EWMA 衰减因子（0.0-1.0），越接近 1 则对最近一次请求的耗时越敏感
	cancel context.CancelFunc     // 用于优雅停止后台自愈任务
}

// NewHealthTracker 创建健康追踪器并开启自愈背景协程。
func NewHealthTracker(alpha float64) *HealthTracker {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.3
	}
	ctx, cancel := context.WithCancel(context.Background())
	ht := &HealthTracker{
		states: make(map[string]*NodeHealth),
		alpha:  alpha,
		cancel: cancel,
	}
	go ht.proactiveSelfHealing(ctx)
	return ht
}

// Close gracefully stops the internal routines
func (ht *HealthTracker) Close() {
	if ht.cancel != nil {
		ht.cancel()
	}
}

// proactiveSelfHealing 启动后台监控，负责将“冷却到期”的熔断节点自动迁移到半开探测状态。
func (ht *HealthTracker) proactiveSelfHealing(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ht.mu.Lock()
			for node, h := range ht.states {
				if h.State == StateOpen {
					// 达到冷却时间后，自动进入半开状态进行探测
					if time.Since(h.LastFailure) > 30*time.Second {
						oldState := h.State.String()
						h.State = StateHalfOpen
						h.ConsecutiveFailures = 0 // 重置计数，给一次机会
						slog.Info("Node entered half-open probe state", "node", node, "prev_state", oldState)
						observability.CircuitBreakerChanges.WithLabelValues(node, oldState+"->HalfOpen").Inc()
					}
				}
			}
			ht.mu.Unlock()
		}
	}
}

// RecordSuccess 记录一次成功的业务调用，并更新统计指标。
// 任何一次成功都会使得节点立即退出熔断状态，回归正常（Closed）。
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

	// 采用 EWMA （指数加权移动平均）计算平均延迟
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
