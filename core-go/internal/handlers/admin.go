package handlers

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"

	"github.com/ai-gateway/core/internal/router"
	"github.com/ai-gateway/core/internal/runtime"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// AdminHandler 承载运行时管理接口。
// 这些接口不参与主请求热路径，但用于运维查看节点、依赖状态和执行人工干预。
type AdminHandler struct {
	router *router.SmartRouter
	rdb    *redis.Client
	status *runtime.SystemStatus
}

// NewAdminHandler 创建管理接口处理器。
func NewAdminHandler(sr *router.SmartRouter, rdb *redis.Client, status *runtime.SystemStatus) *AdminHandler {
	return &AdminHandler{router: sr, rdb: rdb, status: status}
}

// ListNodes 返回当前路由节点及其健康快照，供控制台和运维接口使用。
func (h *AdminHandler) ListNodes(c *gin.Context) {
	nodes := h.router.GetNodes()
	type NodeStatus struct {
		Name    string            `json:"name"`
		Weight  int               `json:"weight"`
		ModelID string            `json:"model_id"`
		Status  router.NodeHealth `json:"status"`
	}

	results := make([]NodeStatus, 0, len(nodes))
	for _, n := range nodes {
		results = append(results, NodeStatus{
			Name:    n.Name,
			Weight:  n.Weight,
			ModelID: n.ModelID,
			Status:  h.router.Tracker.GetHealth(n.Name),
		})
	}

	c.JSON(http.StatusOK, gin.H{"nodes": results})
}

// ListStrategies 返回当前已注册的路由策略名称。
func (h *AdminHandler) ListStrategies(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"strategies": h.router.GetStrategies()})
}

// ListDependencies 暴露当前进程维护的依赖状态表。
// 它是 /readyz 的更完整版本，包含 required、failure mode、version 等细节。
func (h *AdminHandler) ListDependencies(c *gin.Context) {
	if h.status == nil {
		c.JSON(http.StatusOK, gin.H{
			"ready":        true,
			"dependencies": []runtime.DependencyStatus{},
		})
		return
	}

	snapshot := h.status.Snapshot()
	deps := make([]runtime.DependencyStatus, 0, len(snapshot))
	for _, dep := range snapshot {
		deps = append(deps, dep)
	}
	sort.Slice(deps, func(i, j int) bool {
		return deps[i].Name < deps[j].Name
	})

	c.JSON(http.StatusOK, gin.H{
		"ready":        h.status.Ready(),
		"dependencies": deps,
	})
}

// UpdateNodeWeight 允许运维在运行时调整路由权重。
// 变更通过 SmartRouter 的 CoW 更新机制生效，不需要重启服务。
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
	var newNodes []*router.ModelNode
	for _, n := range nodes {
		if n.Name == nodeName {
			clone := *n
			clone.Weight = weight
			newNodes = append(newNodes, &clone)
			found = true
		} else {
			newNodes = append(newNodes, n)
		}
	}

	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "node_not_found"})
		return
	}

	h.router.UpdateNodes(newNodes)
	c.JSON(http.StatusOK, gin.H{"status": "success", "node": nodeName, "new_weight": weight})
}

// ResetQuota 手动清除指定 API Key 的配额累计值。
func (h *AdminHandler) ResetQuota(c *gin.Context) {
	if h.rdb == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "redis_unavailable"})
		return
	}

	targetKey := c.PostForm("api_key")
	if targetKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_api_key"})
		return
	}

	if matched, _ := regexp.MatchString(`^[a-zA-Z0-9_\-]+$`, targetKey); !matched {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_api_key_format"})
		return
	}

	redisKey := fmt.Sprintf("quota:usage:%s", targetKey)
	if err := h.rdb.Del(c.Request.Context(), redisKey).Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis_del_failed", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "quota_reset_successfully"})
}
