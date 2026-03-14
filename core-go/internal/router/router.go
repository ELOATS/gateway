// Package router 提供 AI 模型节点的路由与选择策略。
package router

import (
	"sync"
	"time"

	"github.com/ai-gateway/core/internal/adapters"
)

type Candidate struct {
	Name    string
	Weight  int
	Adapter adapters.Provider
}

type SmartRouter struct {
	mu         sync.RWMutex
	candidates []Candidate
}

// NewSmartRouter 创建一个新的智能路由器实例。
func NewSmartRouter(candidates []Candidate) *SmartRouter {
	return &SmartRouter{candidates: candidates}
}

// Route 基于权重随机算法（Weighted Random）选择最优的 AI 节点。
// 该算法通过计算总权重并取随机数，确保高权重节点获得更多流量。
func (sr *SmartRouter) Route() (string, adapters.Provider) {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	if len(sr.candidates) == 0 { return "", nil }
	var total int
	for _, c := range sr.candidates { total += c.Weight }
	if total <= 0 { return sr.candidates[0].Name, sr.candidates[0].Adapter }
	n := time.Now().UnixNano() % int64(total)
	var curr int64
	for _, c := range sr.candidates {
		curr += int64(c.Weight)
		if n < curr { return c.Name, c.Adapter }
	}
	return sr.candidates[0].Name, sr.candidates[0].Adapter
}

// UpdateCandidates 允许以线程安全的方式动态更新路由表。
func (sr *SmartRouter) UpdateCandidates(newCandidates []Candidate) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.candidates = newCandidates
}
