package handlers

import (
	pb "github.com/ai-gateway/core/api/gateway/v1"
	appchat "github.com/ai-gateway/core/internal/application/chat"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/db"
	"github.com/ai-gateway/core/internal/nitro"
	"github.com/ai-gateway/core/internal/pipeline"
	"github.com/ai-gateway/core/internal/router"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// ChatHandler 负责处理 HTTP 层的请求进入。
// 设计原则：保持 Handler 层尽可能的轻薄（Thin Handler），仅负责参数注入，
// 将核心的业务编排逻辑完全委托给 application 层的 Service 处理，以实现关注点分离。
type ChatHandler struct {
	service *appchat.Service // 注入的聊天补全业务服务
}

// NewChatHandler 负责组装 ChatHandler 的整个依赖树。
// 它在此处将 Pipeline 这一重型编排器实例化并传递给 Service，完成领域对象到应用服务的转换。
func NewChatHandler(ic pb.AiLogicClient, nc nitro.NitroClient, sr *router.SmartRouter, tm db.TenantManager, ce db.CostEngine, rdb *redis.Client, cfg *config.Config) *ChatHandler {
	flow := pipeline.NewChatPipeline(ic, nc, sr, tm, ce, rdb, cfg)
	return &ChatHandler{service: appchat.NewService(flow, cfg)}
}

// HandleChatCompletions 是聊天补全接口的入口函数。
// 它负责从 Gin 上下文提取请求信息，并触发底层的业务执行链路。
func (h *ChatHandler) HandleChatCompletions(c *gin.Context) {
	h.service.HandleChatCompletions(c)
}
