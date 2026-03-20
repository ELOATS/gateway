package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/ai-gateway/core/internal/router"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// AdminHandler 提供运维管理相关的接口。
type AdminHandler struct {
	router *router.SmartRouter
	rdb    *redis.Client
}

// NewAdminHandler 创建一个新的 AdminHandler 实例。
func NewAdminHandler(sr *router.SmartRouter, rdb *redis.Client) *AdminHandler {
	return &AdminHandler{router: sr, rdb: rdb}
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

// UpdateNodeWeight 动态修改指定节点的权重。
func (h *AdminHandler) UpdateNodeWeight(c *gin.Context) {
	nodeName := c.Param("name")
	weightStr := c.PostForm("weight")
	weight, err := strconv.Atoi(weightStr)
	if err != nil || weight < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_weight"})
		return
	}

	nodes := h.router.GetNodes()
	found := false
	for _, n := range nodes {
		if n.Name == nodeName {
			n.Weight = weight
			found = true
			break
		}
	}

	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "node_not_found"})
		return
	}

	h.router.UpdateNodes(nodes) // 触发 CoW 更新
	c.JSON(http.StatusOK, gin.H{"status": "success", "node": nodeName, "new_weight": weight})
}

// ResetQuota 手动重置指定 API Key 的 Redis 每日配额池。
func (h *AdminHandler) ResetQuota(c *gin.Context) {
	targetKey := c.PostForm("api_key")
	if targetKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_api_key"})
		return
	}

	redisKey := fmt.Sprintf("quota:usage:%s", targetKey)
	err := h.rdb.Del(c.Request.Context(), redisKey).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis_del_failed", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "quota_reset_successfully"})
}
