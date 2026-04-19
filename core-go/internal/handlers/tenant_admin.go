package handlers

import (
	"net/http"

	"github.com/ai-gateway/core/internal/db"
	"github.com/gin-gonic/gin"
)

// TenantAdminHandler 提供租户及其关联资源的后台管理接口。
// 它允许管理员进行租户开通、Key 管控以及模型定价调整。
type TenantAdminHandler struct {
	tm db.TenantManager // 租户资源管理器
	ce db.CostEngine    // 计费与成本计算引擎
}

// NewTenantAdminHandler 创建一个新的租户管理处理器实例。
// 该处理器集成了租户生命周期管理与成本计费引擎。
func NewTenantAdminHandler(tm db.TenantManager, ce db.CostEngine) *TenantAdminHandler {
	return &TenantAdminHandler{tm: tm, ce: ce}
}

// ListTenants 返回所有注册租户及其关联的 API Key 与配额。
// 设计决策：通过 GORM Preload 一次性加载关联资源，适应管理后台较低频但高信息密度的访问需求。
func (h *TenantAdminHandler) ListTenants(c *gin.Context) {
	var tenants []db.Tenant
	if err := db.GlobalDB.Preload("APIKeys").Preload("Quotas").Find(&tenants).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list tenants"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tenants": tenants})
}

func (h *TenantAdminHandler) CreateTenant(c *gin.Context) {
	var body struct {
		Name string `json:"name" binding:"required"`
		Tier string `json:"tier"`
	}

	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	tenant := db.Tenant{
		Name: body.Name,
		Tier: body.Tier,
	}
	if tenant.Tier == "" {
		tenant.Tier = "free"
	}

	if err := db.GlobalDB.Create(&tenant).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create tenant", "details": err.Error()})
		return
	}

	// 变更通过 TenantManager 触发现实内存缓存刷新，保证规则立即生效。
	_ = h.tm.RefreshCache()

	c.JSON(http.StatusCreated, tenant)
}

// CreateAPIKey 为指定租户下发一个新的 API Key。
// 设计决策：Key 的生成应由调用方负责，网关仅负责记录其元数据与权限标签。
func (h *TenantAdminHandler) CreateAPIKey(c *gin.Context) {
	tenantID := c.Param("id")
	var body struct {
		Key   string `json:"key" binding:"required"`
		Label string `json:"label" binding:"required"`
	}

	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var tenant db.Tenant
	if err := db.GlobalDB.First(&tenant, tenantID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tenant not found"})
		return
	}

	apiKey := db.APIKey{
		TenantID: tenant.ID,
		Key:      body.Key,
		Label:    body.Label,
		IsActive: true,
	}

	if err := db.GlobalDB.Create(&apiKey).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create API key", "details": err.Error()})
		return
	}

	_ = h.tm.RefreshCache()
	c.JSON(http.StatusCreated, apiKey)
}

// ListModelPrices 返回系统当前所有已定义的模型定价规则。
func (h *TenantAdminHandler) ListModelPrices(c *gin.Context) {
	var prices []db.ModelPrice
	if err := db.GlobalDB.Find(&prices).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list prices"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"prices": prices})
}

// UpdateModelPrice 创建或更新特定模型的单位 Token 价格。
// 修改后会立即通过 CostEngine 刷新内存价格缓存，确保实时计费的准确性。
func (h *TenantAdminHandler) UpdateModelPrice(c *gin.Context) {
	var body struct {
		ModelName   string  `json:"model_name" binding:"required"`
		InputPrice  float64 `json:"input_price"`
		OutputPrice float64 `json:"output_price"`
	}

	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	price := db.ModelPrice{
		ModelName:   body.ModelName,
		InputPrice:  body.InputPrice,
		OutputPrice: body.OutputPrice,
	}

	if err := db.GlobalDB.Save(&price).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update price", "details": err.Error()})
		return
	}

	if h.ce != nil {
		_ = h.ce.RefreshPrices()
	}

	c.JSON(http.StatusOK, price)
}
