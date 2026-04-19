package db

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"
)

// TenantManager 定义了网关多租户管理的核心契约。
// 它负责从持久化层（数据库）加载租户配置与其绑定的 API Key，并提供高性能的内存检索能力。
type TenantManager interface {
	// GetTenantByKey 通过原始 API Key 检索租户信息与 Key 实体。
	// 此方法是热路径，通常由鉴权中间件在每请求级别调用。
	GetTenantByKey(apiKey string) (*Tenant, *APIKey, error)

	// GetTenantByID 通过 ID 直接获取租户详情（含配额定义）。
	// 用于策略引擎执行配额校验。
	GetTenantByID(id uint) (*Tenant, error)

	// RefreshCache 手动触发从数据库同步全量租户数据到内存。
	RefreshCache() error
}

// defaultTenantManager 实现了 TenantManager 接口。
// 设计决策：
// 1. 内存优先：为了支撑万级 QPS，网关不接受在路由主路径上通过 SQL 查询进行鉴权。
// 2. 最终一致性：系统采用“定时全量拉取 + 内存快照切换”的方式实现配置更新。
// 3. 读写分离：使用 sync.RWMutex 保护内部 map，确保护核路径的并发性能。
type defaultTenantManager struct {
	db      *gorm.DB
	cache   sync.RWMutex
	keys    map[string]*APIKey // apiKey 字符串到 APIKey 实体的映射
	tenants map[uint]*Tenant   // tenantID 到 Tenant 实体（含预加载配额）的映射
}

// 确保 defaultTenantManager 实现了 TenantManager 接口，如果不实现会编译报错
var _ TenantManager = (*defaultTenantManager)(nil)

// NewTenantManager 构造一个租户管理器实例，并自动拉起后台同步协程。
func NewTenantManager(db *gorm.DB) TenantManager {
	tm := &defaultTenantManager{
		db:      db,
		keys:    make(map[string]*APIKey),
		tenants: make(map[uint]*Tenant),
	}

	// 启动时立即尝试进行一次全量同步，确保网关具备初始服务能力。
	if err := tm.RefreshCache(); err != nil {
		slog.Error("failed to init tenant cache", "error", err)
	}

	go tm.backgroundSync()
	return tm
}

// backgroundSync 周期性地更新内存映射表。
// 目前设定为每分钟同步一次，这定义了配置变更（如禁用某 Key）的最大理论生效时长。
func (tm *defaultTenantManager) backgroundSync() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if err := tm.RefreshCache(); err != nil {
			slog.Error("failed to sync tenant cache", "error", err)
		}
	}
}

// RefreshCache 实现全量原子更新决策。
// 它采用“影子加载”策略：先在临时变量中构造完整视图，校验无误后再通过写锁一次性切换。
func (tm *defaultTenantManager) RefreshCache() error {
	var dbKeys []APIKey
	// 仅加载活跃的 Key，实现逻辑上的即时吊销。
	if err := tm.db.Where("is_active = ?", true).Find(&dbKeys).Error; err != nil {
		return err
	}

	var dbTenants []Tenant
	// 预加载配额信息，避免后续在处理请求时发生碎片化的数据库 O(N) 查询。
	if err := tm.db.Preload("Quotas").Find(&dbTenants).Error; err != nil {
		return err
	}

	newKeys := make(map[string]*APIKey)
	for i, k := range dbKeys {
		newKeys[k.Key] = &dbKeys[i]
	}

	newTenants := make(map[uint]*Tenant)
	for i, t := range dbTenants {
		newTenants[t.ID] = &dbTenants[i]
	}

	tm.cache.Lock()
	tm.keys = newKeys
	tm.tenants = newTenants
	tm.cache.Unlock()

	return nil
}

func (tm *defaultTenantManager) GetTenantByKey(apiKey string) (*Tenant, *APIKey, error) {
	tm.cache.RLock()
	defer tm.cache.RUnlock()

	ak, ok := tm.keys[apiKey]
	if !ok {
		return nil, nil, errors.New("invalid or inactive api key")
	}
	tenant, ok := tm.tenants[ak.TenantID]
	if !ok {
		return nil, nil, errors.New("tenant not found for api key")
	}

	return tenant, ak, nil
}

func (tm *defaultTenantManager) GetTenantByID(id uint) (*Tenant, error) {
	tm.cache.RLock()
	defer tm.cache.RUnlock()

	tenant, ok := tm.tenants[id]
	if !ok {
		return nil, errors.New("tenant not found")
	}
	return tenant, nil
}
