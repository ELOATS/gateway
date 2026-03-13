// Package router provides strategies for model selection.
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

func NewSmartRouter(candidates []Candidate) *SmartRouter {
	return &SmartRouter{candidates: candidates}
}

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

// UpdateCandidates allows dynamic updates to the routing table in a thread-safe manner.
func (sr *SmartRouter) UpdateCandidates(newCandidates []Candidate) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.candidates = newCandidates
}
