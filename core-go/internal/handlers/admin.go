package handlers

import (
	"net/http"

	"github.com/ai-gateway/core/internal/router"
	"github.com/gin-gonic/gin"
)

// AdminHandler 提供运维管理相关的接口。
type AdminHandler struct {
	router *router.SmartRouter
}

// NewAdminHandler 创建一个新的 AdminHandler 实例。
func NewAdminHandler(sr *router.SmartRouter) *AdminHandler {
	return &AdminHandler{router: sr}
}

// ListNodes 返回所有节点的详细健康状态。
func (h *AdminHandler) ListNodes(c *gin.Context) {
	nodes := h.router.GetNodes()
	type NodeStatus struct {
		Name   string              `json:"name"`
		Status router.NodeHealth `json:"status"`
	}

	var results []NodeStatus
	for _, n := range nodes {
		results = append(results, NodeStatus{
			Name:   n.Name,
			Status: h.router.Tracker.GetHealth(n.Name),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"nodes": results,
	})
}

// ListStrategies 返回已加载的路由策略列表。
func (h *AdminHandler) ListStrategies(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"strategies": h.router.GetStrategies(),
	})
}
