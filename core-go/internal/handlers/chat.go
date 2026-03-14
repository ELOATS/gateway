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
	"github.com/ai-gateway/core/internal/middleware"
	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
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

// HandleChatCompletions 处理与聊天补全相关的 HTTP 请求。
func (h *ChatHandler) HandleChatCompletions(c *gin.Context) {
	start := time.Now()
	requestID := c.GetString(middleware.RequestIDKey)

	slog.Info("收到请求", "request_id", requestID, "client_ip", c.ClientIP())

	// 1. 解析请求体
	var req models.ChatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		slog.Warn("无效的请求载荷", "request_id", requestID, "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_payload", "message": err.Error()})
		return
	}

	// 2. 初始化上下文（设置超时与追踪 ID）
	ctx := observability.NewOutContext(c.Request.Context(), requestID)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// 3. 提取提示词并异步统计 Token
	prompt := h.extractPrompt(&req)
	h.asyncCountTokens(requestID, prompt, req.Model)

	// 4. 检查语义缓存：如果命中则直接返回
	if h.checkCache(c, ctx, requestID, prompt, req.Model) {
		return
	}

	// 5. 执行安全审计（双阶段：Rust 初筛 + Python 深钻）
	finalPrompt, ok := h.runGuardrails(c, ctx, requestID, prompt, req.Model)
	if !ok {
		return
	}

	// 6. 更新请求提示词并执行路由调度
	if len(req.Messages) > 0 {
		req.Messages[len(req.Messages)-1].Content = finalPrompt
	}

	h.routeAndExecute(c, &req, requestID, start)
}

// extractPrompt 从请求体中提取最后一条用户消息。
func (h *ChatHandler) extractPrompt(req *models.ChatCompletionRequest) string {
	if len(req.Messages) > 0 {
		return req.Messages[len(req.Messages)-1].Content
	}
	return ""
}

// asyncCountTokens 异步调用 Rust 侧的分词引擎统计 Token 消耗。
func (h *ChatHandler) asyncCountTokens(rid, text, model string) {
	go func() {
		tCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		tCtx = observability.NewOutContext(tCtx, rid)
		resp, err := h.nitroClient.CountTokens(tCtx, &pb.TokenRequest{Text: text, Model: model})
		if err == nil {
			observability.TokenUsage.WithLabelValues(model).Add(float64(resp.Count))
			slog.Info("指标已记录", "request_id", rid, "tokens", resp.Count)
		}
	}()
}

// checkCache 尝试从智能层获取语义缓存。
// 如果命中缓存，它将直接向客户端返回响应并返回 true。
func (h *ChatHandler) checkCache(c *gin.Context, ctx context.Context, rid, prompt, model string) bool {
	cacheCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	cacheResp, err := h.intelligenceClient.GetCache(cacheCtx, &pb.CacheRequest{Prompt: prompt})
	if err != nil {
		slog.Warn("缓存服务不可用或超时", "request_id", rid, "error", err)
		return false
	}

	if !cacheResp.Hit {
		return false
	}

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

// runGuardrails 执行双阶段安全审计：
// 1. Nitro (Rust): 极速执行正则表达式脱敏 (PII)。
// 2. Intelligence (Python): 执行深度语义审计（如提示词注入检测）。
func (h *ChatHandler) runGuardrails(c *gin.Context, ctx context.Context, rid, prompt, model string) (string, bool) {
	// 阶段 1: Nitro 加速层 (Rust) - 侧重性能
	nitroCtx, cancelNitro := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancelNitro()
	if rsResp, err := h.nitroClient.CheckInput(nitroCtx, &pb.InputRequest{Prompt: prompt}); err == nil {
		prompt = rsResp.SanitizedPrompt
	} else {
		slog.Warn("Nitro guardrail skip due to error", "request_id", rid, "error", err)
	}

	// 阶段 2: Python 智能层 - 侧重深度审计
	pyCtx, cancelPy := context.WithTimeout(ctx, 1000*time.Millisecond)
	defer cancelPy()
	pyResp, err := h.intelligenceClient.CheckInput(pyCtx, &pb.InputRequest{Prompt: prompt})
	if err != nil {
		slog.Error("智能审计服务异常", "request_id", rid, "error", err)
		return prompt, true // 降级策略：审计服务故障时允许通过
	}

	if !pyResp.Safe {
		slog.Warn("安全拦截", "request_id", rid, "reason", pyResp.Reason)
		c.JSON(http.StatusForbidden, gin.H{"error": "security_block", "reason": pyResp.Reason})
		observability.RequestsTotal.WithLabelValues("403", model).Inc()
		return "", false
	}

	return pyResp.SanitizedPrompt, true
}

// routeAndExecute 执行智能路由并调用最终的 AI 模型提供商。
// 路由结果会被反馈到 HealthTracker 以更新节点健康状态。
func (h *ChatHandler) routeAndExecute(c *gin.Context, req *models.ChatCompletionRequest, rid string, start time.Time) {
	// 构造路由上下文。
	routeCtx := &router.RouteContext{
		RequestID:    rid,
		Model:        req.Model,
		PromptTokens: len(h.extractPrompt(req)) / 4, // 粗略估算：4 字符 ≈ 1 token。
		UserTier:     c.GetString("key_label"),
		Headers: map[string]string{
			"X-Route-Strategy": c.GetHeader("X-Route-Strategy"),
		},
	}

	node, err := h.router.Route(routeCtx)
	if err != nil {
		slog.Error("路由失败", "request_id", rid, "error", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "routing_error", "message": err.Error()})
		observability.RequestsTotal.WithLabelValues("503", req.Model).Inc()
		return
	}

	slog.Info("路由分配", "request_id", rid, "node", node.Name, "model_id", node.ModelID)

	// 执行调用并记录健康数据。
	callStart := time.Now()
	resp, err := node.Adapter.ChatCompletion(req)
	callDuration := time.Since(callStart)

	if err != nil {
		h.router.Tracker.RecordFailure(node.Name)
		slog.Error("供应商调用失败", "request_id", rid, "node", node.Name, "error", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "provider_error", "message": err.Error()})
		observability.RequestsTotal.WithLabelValues("502", req.Model).Inc()
		return
	}

	// 记录成功调用的延迟到健康追踪器。
	h.router.Tracker.RecordSuccess(node.Name, callDuration)

	duration := time.Since(start)
	observability.Latency.WithLabelValues(req.Model).Observe(duration.Seconds())
	observability.NodeLatency.WithLabelValues(node.Name).Observe(callDuration.Seconds())
	observability.RequestsTotal.WithLabelValues("200", req.Model).Inc()

	slog.Info("请求完成", "request_id", rid, "node", node.Name, "duration_ms", duration.Milliseconds())
	c.JSON(http.StatusOK, resp)
}
