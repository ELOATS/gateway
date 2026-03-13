// Package handlers implements the HTTP controllers for the AI Gateway.
package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/router"
	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ChatHandler 处理与聊天补全相关的 HTTP 请求。
type ChatHandler struct {
	intelligenceClient pb.AiLogicClient
	nitroClient        pb.AiLogicClient
	router             *router.SmartRouter
}

// NewChatHandler 创建一个包含所需依赖的 ChatHandler 实例。
func NewChatHandler(ic pb.AiLogicClient, nc pb.AiLogicClient, sr *router.SmartRouter) *ChatHandler {
	return &ChatHandler{
		intelligenceClient: ic,
		nitroClient:        nc,
		router:             sr,
	}
}

func (h *ChatHandler) HandleChatCompletions(c *gin.Context) {
	start := time.Now()
	requestID := uuid.New().String()

	slog.Info("Incoming request", "request_id", requestID, "client_ip", c.ClientIP())

	var req models.ChatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		slog.Warn("Invalid payload", "request_id", requestID, "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_payload", "message": err.Error()})
		return
	}

	ctx := observability.NewOutContext(c.Request.Context(), requestID)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	prompt := h.extractPrompt(&req)
	h.asyncCountTokens(requestID, prompt, req.Model)

	if h.checkCache(c, ctx, requestID, prompt, req.Model) {
		return
	}

	finalPrompt, ok := h.runGuardrails(c, ctx, requestID, prompt, req.Model)
	if !ok {
		return
	}

	if len(req.Messages) > 0 {
		req.Messages[len(req.Messages)-1].Content = finalPrompt
	}

	h.routeAndExecute(c, &req, requestID, start)
}

func (h *ChatHandler) extractPrompt(req *models.ChatCompletionRequest) string {
	if len(req.Messages) > 0 {
		return req.Messages[len(req.Messages)-1].Content
	}
	return ""
}

func (h *ChatHandler) asyncCountTokens(rid, text, model string) {
	go func() {
		tCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		tCtx = observability.NewOutContext(tCtx, rid)
		resp, err := h.nitroClient.CountTokens(tCtx, &pb.TokenRequest{Text: text, Model: model})
		if err == nil {
			observability.TokenUsage.WithLabelValues(model).Add(float64(resp.Count))
			slog.Info("Metric recorded", "request_id", rid, "tokens", resp.Count)
		}
	}()
}

func (h *ChatHandler) checkCache(c *gin.Context, ctx context.Context, rid, prompt, model string) bool {
	// 语义缓存通常应在 500ms 内完成，避免拖慢整体响应。
	cacheCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	cacheResp, err := h.intelligenceClient.GetCache(cacheCtx, &pb.CacheRequest{Prompt: prompt})
	if err != nil {
		slog.Warn("Cache service unavailable or timeout", "request_id", rid, "error", err)
		return false
	}

	if cacheResp.Hit {
		slog.Info("Cache Hit", "request_id", rid)
		c.JSON(http.StatusOK, models.ChatCompletionResponse{
			ID: fmt.Sprintf("cache-%s", rid),
			Choices: []models.Choice{{
				Message: models.Message{Role: "assistant", Content: cacheResp.Response},
			}},
		})
		observability.RequestsTotal.WithLabelValues("200_cache", model).Inc()
		return true
	}
	return false
}

func (h *ChatHandler) runGuardrails(c *gin.Context, ctx context.Context, rid, prompt, model string) (string, bool) {
	// Nitro 加速层 (Rust) 应极快，设为 200ms。
	nitroCtx, cancelNitro := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancelNitro()
	rsResp, err := h.nitroClient.CheckInput(nitroCtx, &pb.InputRequest{Prompt: prompt})
	if err == nil {
		prompt = rsResp.SanitizedPrompt
	} else {
		slog.Warn("Nitro guardrail skip due to error", "request_id", rid, "error", err)
	}

	// Python 智能层审计稍微慢一些，设为 1s。
	pyCtx, cancelPy := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancelPy()
	pyResp, err := h.intelligenceClient.CheckInput(pyCtx, &pb.InputRequest{Prompt: prompt})
	if err != nil {
		slog.Error("Intelligence guardrail service error", "request_id", rid, "error", err)
		// 如果安全审计服务挂了，出于安全考虑，这里可以选择报错或继续。
		// 目前选择记录错误并继续，但实际生产环境可能需要更严格的策略。
	} else if !pyResp.Safe {
		slog.Warn("Security Block", "request_id", rid, "reason", pyResp.Reason)
		c.JSON(http.StatusForbidden, gin.H{"error": "security_block", "reason": pyResp.Reason})
		observability.RequestsTotal.WithLabelValues("403", model).Inc()
		return "", false
	} else {
		prompt = pyResp.SanitizedPrompt
	}

	return prompt, true
}

func (h *ChatHandler) routeAndExecute(c *gin.Context, req *models.ChatCompletionRequest, rid string, start time.Time) {
	nodeName, adapter := h.router.Route()
	slog.Info("Routing", "request_id", rid, "node", nodeName)

	resp, err := adapter.ChatCompletion(req)
	if err != nil {
		slog.Error("Provider error", "request_id", rid, "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "provider_error", "message": err.Error()})
		observability.RequestsTotal.WithLabelValues("502", req.Model).Inc()
		return
	}

	duration := time.Since(start)
	observability.Latency.WithLabelValues(req.Model).Observe(duration.Seconds())
	observability.RequestsTotal.WithLabelValues("200", req.Model).Inc()

	slog.Info("Request completed", "request_id", rid, "duration_ms", duration.Milliseconds())
	c.JSON(http.StatusOK, resp)
}
