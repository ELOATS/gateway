package cache

import (
	"context"
	"math"
	"sync"
)

// VectorEntry 表示存储在向量数据库中的单条记录。
type VectorEntry struct {
	ID     string
	Vector []float32
	Data   string // 缓存的响应内容
}

// VectorStore 定义语义缓存底层的存储与检索接口。
type VectorStore interface {
	Save(ctx context.Context, entry VectorEntry) error
	Search(ctx context.Context, vector []float32, threshold float32) (*VectorEntry, error)
}

// MemoryVectorStore 是一个线程安全的在内存实现的简单向量存储。
// 适用于开发环境或低并发场景下的语义缓存。
type MemoryVectorStore struct {
	mu      sync.RWMutex
	entries []VectorEntry
}

func NewMemoryVectorStore() *MemoryVectorStore {
	return &MemoryVectorStore{}
}

func (s *MemoryVectorStore) Save(ctx context.Context, entry VectorEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
	return nil
}

func (s *MemoryVectorStore) Search(ctx context.Context, vector []float32, threshold float32) (*VectorEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var bestMatch *VectorEntry
	var maxScore float32 = -1.0

	for i := range s.entries {
		score := CosineSimilarity(vector, s.entries[i].Vector)
		if score > maxScore {
			maxScore = score
			bestMatch = &s.entries[i]
		}
	}

	if maxScore >= threshold {
		return bestMatch, nil
	}
	return nil, nil
}

// CosineSimilarity 计算两个向量的余弦相似度。
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dotProduct / (math.Sqrt(normA) * math.Sqrt(normB)))
}
