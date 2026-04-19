package db

import (
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"
)

// CostEngine 负责在内存中执行高性能的单位成本计算，并协调账单数据的持久化。
// 它是系统计费逻辑的核心实现，结合动态更新的模型价格表生成消费记录。
type CostEngine interface {
	// CalculateCost 根据模型名和 Token 数计算本次请求的理论成本。
	CalculateCost(model string, inputTokens, outputTokens int) float64
	// RecordUsage 计算最终成本并将 UsageLog 完整记录到持久化存储。
	RecordUsage(log *UsageLog) error
	// RefreshPrices 从数据库全量重新加载最新的模型定价策略。
	RefreshPrices() error
}

// costEngine 实现了 CostEngine 接口。
// 设计决策：
// 1. 读写分离缓存：定价信息存储在内存 map 中，通过 RWMutex 保护，确保在高并发请求下零数据库开销。
// 2. 异步背景刷新：价格表周期性自动更新，在确保定价时效性的同时，避免了人工干预或配置重启。
type costEngine struct {
	db     *gorm.DB
	prices map[string]ModelPrice
	mu     sync.RWMutex
}

// NewCostEngine 构造成本计算引擎实例，并初始化价格缓存与背景刷新任务。
func NewCostEngine(db *gorm.DB) CostEngine {
	ce := &costEngine{
		db:     db,
		prices: make(map[string]ModelPrice),
	}
	// 启动时同步加载一次价格，确保后续计算有据可依。
	_ = ce.RefreshPrices()
	go ce.backgroundRefresh()
	return ce
}

// RefreshPrices 执行原子的价格热切换。
// 它采用全量置换策略，确保价格更新期间的一致性。
func (c *costEngine) RefreshPrices() error {
	var prices []ModelPrice
	if err := c.db.Find(&prices).Error; err != nil {
		slog.Error("failed to load model prices", "error", err)
		return err
	}

	priceMap := make(map[string]ModelPrice)
	for _, p := range prices {
		priceMap[p.ModelName] = p
	}

	c.mu.Lock()
	c.prices = priceMap
	c.mu.Unlock()
	return nil
}

// backgroundRefresh 定义了定价策略的自动更新频率。
func (c *costEngine) backgroundRefresh() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		_ = c.RefreshPrices()
	}
}

// CalculateCost 执行单位 Token 的加权成本计算。
func (c *costEngine) CalculateCost(model string, inputTokens, outputTokens int) float64 {
	c.mu.RLock()
	price, exists := c.prices[model]
	c.mu.RUnlock()

	if !exists {
		// 容错逻辑：对于未定价的实验性模型，记录 0 成本但不拦截服务，避免阻塞业务。
		return 0
	}

	// 计费公式：(输入/1000 * 输入单价) + (输出/1000 * 输出单价)
	inputCost := (float64(inputTokens) / 1000.0) * price.InputPrice
	outputCost := (float64(outputTokens) / 1000.0) * price.OutputPrice

	return inputCost + outputCost
}

// RecordUsage 将单次请求的消费明细持久化。
func (c *costEngine) RecordUsage(log *UsageLog) error {
	log.Cost = c.CalculateCost(log.Model, log.InputTokens, log.OutputTokens)
	// 这里目前采用同步写入，对于超大规模并发场景，未来可考虑引入内存 buffer 或 Kafka 削峰。
	if err := c.db.Create(log).Error; err != nil {
		slog.Error("failed to record usage log", "request_id", log.RequestID, "error", err)
		return err
	}
	return nil
}
