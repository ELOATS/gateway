package billing

import (
	"time"

	"github.com/ai-gateway/core/internal/db"
	"gorm.io/gorm"
)

// TenantBillingSummary 代表租户的计费摘要
type TenantBillingSummary struct {
	TenantID     uint      `json:"tenant_id"`
	TenantName   string    `json:"tenant_name"`
	TotalCost    float64   `json:"total_cost"`
	TotalTokens  int       `json:"total_tokens"`
	RequestCount int64     `json:"request_count"`
	LastActivity time.Time `json:"last_activity"`
}

// ModelUsageSummary 代表具体模型的消费情况
type ModelUsageSummary struct {
	Model        string  `json:"model"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalCost    float64 `json:"total_cost"`
	RequestCount int64   `json:"request_count"`
}

// BillingService 提供多维度的计费数据分析与财务统计功能。
// 它解耦了底层数据库的聚合查询逻辑，为管理后台与网关状态面板提供一致的消费视图。
type BillingService struct {
	db *gorm.DB
}

// NewBillingService 构造计费服务实例。
func NewBillingService(dbConn *gorm.DB) *BillingService {
	// 执行自动迁移 (Auto Migration)
	// 设计原则：
	// 1. 声明式同步：通过 GORM 定义的 Tag 自动维护数据库表结构，确保代码与 Schema 的原子性一致。
	// 2. 演进稳定性：在多租户模型 Price 表、配额 Quota 表等核心资产变更时，能够平滑过渡且不丢失业务数据。
	dbConn.AutoMigrate(&db.Tenant{}, &db.APIKey{}, &db.Quota{}, &db.UsageLog{}, &db.ModelPrice{})
	return &BillingService{db: dbConn}
}

// GetTenantSummary 获取指定租户的财务总览。
// 该方法聚合了从租户创建至今的所有消费记录，是衡量租户价值与额度消耗的关键。
func (s *BillingService) GetTenantSummary(tenantID uint) (*TenantBillingSummary, error) {
	var tenant db.Tenant
	if err := s.db.First(&tenant, tenantID).Error; err != nil {
		return nil, err
	}

	var stats struct {
		TotalCost    float64
		InputTokens  int
		OutputTokens int
		RequestCount int64
		LastActivity time.Time
	}

	err := s.db.Model(&db.UsageLog{}).
		Select("SUM(cost) as total_cost, SUM(input_tokens) as input_tokens, SUM(output_tokens) as output_tokens, COUNT(*) as request_count, MAX(created_at) as last_activity").
		Where("tenant_id = ?", tenantID).
		Scan(&stats).Error

	if err != nil {
		return nil, err
	}

	return &TenantBillingSummary{
		TenantID:     tenantID,
		TenantName:   tenant.Name,
		TotalCost:    stats.TotalCost,
		TotalTokens:  stats.InputTokens + stats.OutputTokens,
		RequestCount: stats.RequestCount,
		LastActivity: stats.LastActivity,
	}, nil
}

// GetGlobalModelUsage 提供全局模型消耗热度分析。
// 返回按模型名称分组的 Token 消耗、请求量与总成本，用于后端供应商的容量规划与成本管控。
func (s *BillingService) GetGlobalModelUsage() ([]ModelUsageSummary, error) {
	var usage []ModelUsageSummary
	err := s.db.Model(&db.UsageLog{}).
		Select("model, SUM(input_tokens) as input_tokens, SUM(output_tokens) as output_tokens, SUM(cost) as total_cost, COUNT(*) as request_count").
		Group("model").
		Scan(&usage).Error

	return usage, err
}

// ListAllTenantsUsage 批量拉取所有租户的消费摘要。
// 用于管理控制台的全局租户排名与欠费预警。
func (s *BillingService) ListAllTenantsUsage() ([]TenantBillingSummary, error) {
	var summaries []TenantBillingSummary
	err := s.db.Table("tenants").
		Select("tenants.id as tenant_id, tenants.name as tenant_name, SUM(usage_logs.cost) as total_cost, SUM(usage_logs.input_tokens + usage_logs.output_tokens) as total_tokens, COUNT(usage_logs.id) as request_count").
		Joins("LEFT JOIN usage_logs ON usage_logs.tenant_id = tenants.id").
		Group("tenants.id").
		Scan(&summaries).Error

	return summaries, err
}
