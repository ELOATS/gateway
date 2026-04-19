package handlers

import (
	"net/http"
	"strconv"

	"github.com/ai-gateway/core/internal/application/billing"
	"github.com/gin-gonic/gin"
)

// BillingHandler 提供计费相关的 HTTP 接口，用于汇总和查阅各租户及模型的消费情况。
type BillingHandler struct {
	service *billing.BillingService // 注入计费业务逻辑服务
}

func NewBillingHandler(s *billing.BillingService) *BillingHandler {
	return &BillingHandler{service: s}
}

// GetTenantSummary 返回单个租户的详细消费摘要。
func (h *BillingHandler) GetTenantSummary(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tenant id"})
		return
	}

	summary, err := h.service.GetTenantSummary(uint(id))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get tenant summary", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, summary)
}

// GetModelUsageReport 返回按物理模型划分的全局消费报表，用于分析供应商支出。
func (h *BillingHandler) GetModelUsageReport(c *gin.Context) {
	usage, err := h.service.GetGlobalModelUsage()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get model usage report", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"models": usage})
}

// GetAllTenantsUsage 返回系统中所有租户的消费摘要全集。
func (h *BillingHandler) GetAllTenantsUsage(c *gin.Context) {
	summaries, err := h.service.ListAllTenantsUsage()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list all tenants usage", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"tenants": summaries})
}
