package router

import (
	"sync"
	"time"
)

// NodeHealth 记录单个模型节点的实时运行状态。
// 延迟使用 EWMA（指数加权移动平均）计算，兼顾实时性与平滑性。
type NodeHealth struct {
	AvgLatency    float64   // EWMA 延迟，单位秒。
	ErrorRate     float64   // 最近统计周期内的错误率 (0.0~1.0)。
	LastSuccess   time.Time // 最后一次成功调用的时间。
	LastFailure   time.Time // 最后一次失败调用的时间。
	TotalRequests int64     // 累计请求数。
	TotalErrors   int64     // 累计错误数。
}

// IsHealthy 综合判定节点是否健康。
// 规则：错误率 < 50% 且最近 60 秒内有成功记录（或从未调用过）。
func (nh *NodeHealth) IsHealthy() bool {
	// 从未被调用过的节点视为健康。
	if nh.TotalRequests == 0 {
		return true
	}

	// 错误率超过 50% 视为不健康。
	if nh.ErrorRate > 0.5 {
		return false
	}

	// 最后一次成功已超过 60 秒，且存在失败记录，视为不健康。
	if !nh.LastFailure.IsZero() && time.Since(nh.LastSuccess) > 60*time.Second {
		return false
	}

	return true
}

// HealthTracker 维护所有节点的实时健康状态。
// 使用 EWMA 算法追踪延迟变化趋势，alpha 越大越重视最新数据。
type HealthTracker struct {
	mu     sync.RWMutex
	states map[string]*NodeHealth
	alpha  float64 // EWMA 衰减因子（推荐 0.3）。
}

// NewHealthTracker 创建一个新的健康追踪器实例。
// alpha 控制 EWMA 的衰减速率：0.3 表示 30% 权重给最新数据，70% 给历史数据。
func NewHealthTracker(alpha float64) *HealthTracker {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.3
	}
	return &HealthTracker{
		states: make(map[string]*NodeHealth),
		alpha:  alpha,
	}
}

// RecordSuccess 记录一次成功的调用及其延迟。
func (ht *HealthTracker) RecordSuccess(node string, latency time.Duration) {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	h := ht.getOrCreate(node)
	h.TotalRequests++
	h.LastSuccess = time.Now()

	// EWMA 更新延迟
	lat := latency.Seconds()
	if h.AvgLatency == 0 {
		h.AvgLatency = lat // 首次记录直接赋值。
	} else {
		h.AvgLatency = ht.alpha*lat + (1-ht.alpha)*h.AvgLatency
	}

	// 重新计算错误率
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

	// 重新计算错误率
	h.ErrorRate = float64(h.TotalErrors) / float64(h.TotalRequests)
}

// GetHealth 获取指定节点的健康快照（只读副本）。
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
		return true // 从未被追踪的节点视为健康。
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
