package db

import (
	"time"

	"gorm.io/gorm"
)

// Tenant 表示一个使用网关服务的逻辑隔离单元（租户）。
// 它是权限管理和计费的最顶层账户实体。
type Tenant struct {
	gorm.Model
	Name    string   `gorm:"uniqueIndex;not null"` // 租户全称，必须全局唯一
	Tier    string   `gorm:"default:'free'"`       // 客户等级：free (免费), standard (标准), enterprise (企业)
	APIKeys []APIKey // 该租户拥有的所有用于身份验证的 API Key
	Quotas  []Quota  // 该租户全局生效的频次或 Token 配额限制
}

// APIKey 代表属于特定租户的具体访问令牌。
// 用户通过携带此 Key 在 Header 中来向网关证明其所属租户身份。
type APIKey struct {
	gorm.Model
	TenantID uint   // 所属租户 ID
	Key      string `gorm:"uniqueIndex;not null"` // 加密混淆后的原始 Key 字符串
	Label    string `gorm:"default:'default'"`    // 别名（如 "测试环境专用"），便于用户管理
	IsActive bool   `gorm:"default:true"`         // 开关标志，设为 false 可立即撤销该 Key 的访问权
}

// Quota 描述了针对租户或特定 API Key 的流量管控规则。
type Quota struct {
	gorm.Model
	TenantID   uint
	LimitType  string `gorm:"not null"`        // 限制类型：token (令牌数), request (请求次数)
	Value      int64  `gorm:"not null"`        // 窗口期内的最大阈值
	TimeWindow string `gorm:"default:'daily'"` // 管控周期：daily (日), monthly (月), all_time (永久总量)
}

// ModelPrice 存储了不同模型在不同供应商处的单位定价信息。
type ModelPrice struct {
	ModelName   string    `gorm:"primaryKey;index"`            // 物理模型名称（如 gpt-4）
	InputPrice  float64   `gorm:"not null;type:decimal(10,6)"` // 每一千输入 tokens 的成本（USD/CNY）
	OutputPrice float64   `gorm:"not null;type:decimal(10,6)"` // 每一千输出 tokens 的成本（USD/CNY）
	UpdatedAt   time.Time // 上次调价时间，用于成本回溯
}

// UsageLog 记录了网关执行成功的每一次请求的详细消费明细。
// 此表是系统财务对账、用户消费报表及成本归因的核心数据来源。
type UsageLog struct {
	ID           uint      `gorm:"primarykey"`
	RequestID    string    `gorm:"index"`                       // 网关 RequestID，用于链路追踪
	TenantID     uint      `gorm:"index"`                       // 请求所属租户
	APIKeyID     uint      `gorm:"index"`                       // 发起请求的具体 Key ID
	Model        string    `gorm:"index"`                       // 实际响应的远程物理模型名
	Provider     string    `gorm:"index"`                       // 服务提供商 (openai/anthropic等)
	InputTokens  int       `gorm:"not null"`                    // 消耗的输入 Token
	OutputTokens int       `gorm:"not null"`                    // 消耗的输出 Token
	Cost         float64   `gorm:"not null;type:decimal(10,6)"` // 本次请求产生的实付成本（基于 ModelPrice 计算）
	CreatedAt    time.Time `gorm:"index"`                       // 记录生成时间
}
