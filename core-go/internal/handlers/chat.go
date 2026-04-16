package handlers

import (
	pb "github.com/ai-gateway/core/api/gateway/v1"
	appchat "github.com/ai-gateway/core/internal/application/chat"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/nitro"
	"github.com/ai-gateway/core/internal/pipeline"
	"github.com/ai-gateway/core/internal/router"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// ChatHandler keeps the HTTP surface thin and delegates orchestration to the application layer.
type ChatHandler struct {
	service *appchat.Service
}

func NewChatHandler(ic pb.AiLogicClient, nc nitro.NitroClient, sr *router.SmartRouter, rdb *redis.Client, cfg *config.Config) *ChatHandler {
	flow := pipeline.NewChatPipeline(ic, nc, sr, rdb, cfg)
	return &ChatHandler{service: appchat.NewService(flow, cfg)}
}

func (h *ChatHandler) HandleChatCompletions(c *gin.Context) {
	h.service.HandleChatCompletions(c)
}
