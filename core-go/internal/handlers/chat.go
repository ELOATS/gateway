// Package handlers implements the HTTP controllers for the AI Gateway.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ai-gateway/core/internal/config"
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
	config             *config.Config
}

// NewChatHandler 创建一个包含所需依赖的 ChatHandler 实例。
func NewChatHandler(ic pb.AiLogicClient, nc pb.AiLogicClient, sr *router.SmartRouter, cfg *config.Config) *ChatHandler {
	return &ChatHandler{
		intelligenceClient: ic,
		nitroClient:        nc,
		router:             sr,
		config:             cfg,
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
	ctx, cancel := context.WithTimeout(ctx, h.config.RequestTimeout)
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

	if len(req.Messages) > 0 {
		req.Messages[len(req.Messages)-1].Content = finalPrompt
	}

	// 6. 判定响应模式：流式还是阻塞
	if req.Stream {
		h.streamExecute(c, ctx, &req, requestID, start)
	} else {
		h.routeAndExecute(c, ctx, &req, requestID, start)
	}
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
		tCtx, cancel := context.WithTimeout(context.Background(), h.config.TokenCountTimeout)
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
	cacheCtx, cancel := context.WithTimeout(ctx, h.config.CacheTimeout)
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
	nitroCtx, cancelNitro := context.WithTimeout(ctx, h.config.GuardrailNitroTimeout)
	defer cancelNitro()
	if rsResp, err := h.nitroClient.CheckInput(nitroCtx, &pb.InputRequest{Prompt: prompt}); err == nil {
		prompt = rsResp.SanitizedPrompt
	} else {
		slog.Warn("Nitro guardrail skip due to error", "request_id", rid, "error", err)
	}

	// 阶段 2: Python 智能层 - 侧重深度审计
	pyCtx, cancelPy := context.WithTimeout(ctx, h.config.GuardrailIntellTimeout)
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

// runOutputGuardrails 对模型生成的响应进行安全合规审计。
func (h *ChatHandler) runOutputGuardrails(ctx context.Context, rid, text, model string) (string, bool) {
	// 阶段 1: Python 智能层 - 执行输出合规审计（如幻觉、敏感信息检测）
	pyCtx, cancelPy := context.WithTimeout(ctx, h.config.GuardrailIntellTimeout)
	defer cancelPy()

	pyResp, err := h.intelligenceClient.CheckOutput(pyCtx, &pb.OutputRequest{ResponseText: text})
	if err != nil {
		slog.Warn("输出审计服务不可用或超时，降级放行", "request_id", rid, "error", err)
		return text, true
	}

	if !pyResp.Safe {
		slog.Warn("输出安全拦截", "request_id", rid)
		return pyResp.SanitizedText, true // 或者返回自定义错误。这里选择返回被脱敏/说明后的文本。
	}

	return pyResp.SanitizedText, true
}

// streamExecute 执行流式转发逻辑。
func (h *ChatHandler) streamExecute(c *gin.Context, ctx context.Context, req *models.ChatCompletionRequest, rid string, start time.Time) {
	// 1. 路由选择（流式暂不支持重试，因为首字节返回后状态码已发送）
	routeCtx := &router.RouteContext{
		RequestID:    rid,
		Model:        req.Model,
		PromptTokens: len(h.extractPrompt(req)) / h.config.TokenEstimationFactor,
		UserTier:     c.GetString("key_label"),
		Headers:      map[string]string{"X-Route-Strategy": c.GetHeader("X-Route-Strategy")},
	}

	node, err := h.router.Route(routeCtx)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "routing_error", "message": err.Error()})
		return
	}

	// 2. 调用流式接口
	respCh, errCh := node.Adapter.ChatCompletionStream(req)

	// 3. 设置 SSE 响应头
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Flush()

	// 4. 流式转发循环
	slog.Info("流式传输开始", "request_id", rid, "node", node.Name)
	
	flusher := c.Writer
	h.router.Tracker.RecordSuccess(node.Name, 0) // 流式记为成功，延迟统计到结束

	for {
		select {
		case <-ctx.Done():
			slog.Warn("客户端断开连接或超时", "request_id", rid)
			return
		case err := <-errCh:
			if err != nil {
				slog.Error("供应商流损毁", "request_id", rid, "error", err)
				fmt.Fprintf(c.Writer, "data: {\"error\": \"stream_error\", \"message\": \"%s\"}\n\n", err.Error())
				return
			}
		case streamResp, ok := <-respCh:
			if !ok {
				fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
				slog.Info("流式传输完成", "request_id", rid, "duration_ms", time.Since(start).Milliseconds())
				return
			}

			data, _ := json.Marshal(streamResp)
			fmt.Fprintf(c.Writer, "data: %s\n\n", string(data))
			flusher.Flush()
		}
	}
}

// routeAndExecute 执行智能路由并调用最终的 AI 模型提供商。
// 路由结果会被反馈到 HealthTracker 以更新节点健康状态。
// P1 阶段重构：增加了基于排除列表的循环重试逻辑。
func (h *ChatHandler) routeAndExecute(c *gin.Context, ctx context.Context, req *models.ChatCompletionRequest, rid string, start time.Time) {
	var excluded []string
	maxRetries := h.config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// 1. 构造路由上下文。
		routeCtx := &router.RouteContext{
			RequestID:    rid,
			Model:        req.Model,
			PromptTokens: len(h.extractPrompt(req)) / h.config.TokenEstimationFactor,
			UserTier:     c.GetString("key_label"),
			Headers: map[string]string{
				"X-Route-Strategy": c.GetHeader("X-Route-Strategy"),
			},
			ExcludeNodes: excluded,
		}

		// 2. 路由选择。
		node, err := h.router.Route(routeCtx)
		if err != nil {
			slog.Error("路由失败", "request_id", rid, "attempt", attempt, "error", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "routing_error", "message": err.Error()})
			observability.RequestsTotal.WithLabelValues("503", req.Model).Inc()
			return
		}

		if attempt > 0 {
			slog.Info("正在重试路由", "request_id", rid, "attempt", attempt, "next_node", node.Name)
		} else {
			slog.Info("路由分配", "request_id", rid, "node", node.Name, "model_id", node.ModelID)
		}

		// 3. 执行供应商调用。
		callStart := time.Now()
		resp, err := node.Adapter.ChatCompletion(req)
		callDuration := time.Since(callStart)

		if err != nil {
			h.router.Tracker.RecordFailure(node.Name)
			slog.Error("供应商调用失败", "request_id", rid, "node", node.Name, "error", err)

			if attempt < maxRetries {
				excluded = append(excluded, node.Name)
				continue
			}

			c.JSON(http.StatusBadGateway, gin.H{"error": "provider_error", "message": err.Error()})
			observability.RequestsTotal.WithLabelValues("502", req.Model).Inc()
			return
		}

		// 4. 记录调用反馈。
		h.router.Tracker.RecordSuccess(node.Name, callDuration)

		// 5. 执行输出审计 (Output Guardrails)。
		finalText := resp.Choices[0].Message.Content
		if len(resp.Choices) > 0 {
			auditedText, _ := h.runOutputGuardrails(ctx, rid, finalText, node.ModelID)
			resp.Choices[0].Message.Content = auditedText
		}

		// 6. 记录指标并返回。
		duration := time.Since(start)
		observability.Latency.WithLabelValues(req.Model).Observe(duration.Seconds())
		observability.NodeLatency.WithLabelValues(node.Name).Observe(callDuration.Seconds())
		observability.RequestsTotal.WithLabelValues("200", req.Model).Inc()

		slog.Info("请求完成", "request_id", rid, "node", node.Name, "duration_ms", duration.Milliseconds(), "attempts", attempt+1)
		c.JSON(http.StatusOK, resp)
		return
	}
}
