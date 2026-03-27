package routes

import (
	"net/http"

	"github.com/ai-gateway/core/internal/runtime"
	"github.com/gin-gonic/gin"
)

// installStatusRoutes 安装健康检查相关端点。
// /healthz 只表示进程存活，/readyz 则反映必需依赖是否健康。
func installStatusRoutes(r *gin.Engine, status *runtime.SystemStatus) {
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "alive"})
	})

	r.GET("/readyz", func(c *gin.Context) {
		if status == nil {
			// 没有状态表时退回到“始终 ready”的兼容模式。
			c.JSON(http.StatusOK, gin.H{"status": "ready"})
			return
		}

		ready := status.Ready()
		payload := gin.H{
			"status":       "ready",
			"dependencies": status.Snapshot(),
		}
		if !ready {
			payload["status"] = "not_ready"
			c.JSON(http.StatusServiceUnavailable, payload)
			return
		}
		c.JSON(http.StatusOK, payload)
	})

	r.GET("/health", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/healthz")
	})
}
