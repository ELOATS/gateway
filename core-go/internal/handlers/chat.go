// Package handlers implements the HTTP controllers for the AI Gateway.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/middleware"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/router"
	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// ChatHandler 处理与聊天补全相关的 HTTP 请求。
type ChatHandler struct {
	intelligenceClient pb.AiLogicClient
	nitroClient        pb.AiLogicClient
	router             *router.SmartRouter
	config             *config.Config
	rdb                *redis.Client // Redis 客户端，用于更新配额
	semaphore          chan struct{} // 核心层并发保护信号量
}

// NewChatHandler 创建一个包含所需依赖的 ChatHandler 实例。
func NewChatHandler(ic pb.AiLogicClient, nc pb.AiLogicClient, sr *router.SmartRouter, rdb *redis.Client, cfg *config.Config) *ChatHandler {
	limit := cfg.MaxConcurrentRequests
	if limit <= 0 {
		limit = 1000
	}
	return &ChatHandler{
		intelligenceClient: ic,
		nitroClient:        nc,
		router:             sr,
		config:             cfg,
		rdb:                rdb,
		semaphore:          make(chan struct{}, limit),
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
func (h *ChatHandler) checkCache(c *gin.Context, ctx context.Context, rid, prompt, model string) bool {
	cacheCtx, cancel := context.WithTimeout(ctx, h.config.CacheTimeout)
	defer cancel()

	cacheResp, err := h.intelligenceClient.GetCache(cacheCtx, &pb.CacheRequest{
		Prompt: prompt,
		Model:  model,
	})
	if err != nil {
		slog.Warn("缓存服务不可用或超时", "request_id", rid, "error", err)
		return false
	}

	if !cacheResp.Hit {
		observability.CacheHitsTotal.WithLabelValues("miss", model).Inc()
		return false
	}

	slog.Info("Cache Hit", "request_id", rid)
	observability.CacheHitsTotal.WithLabelValues("hit", model).Inc()
	c.JSON(http.StatusOK, models.ChatCompletionResponse{
		ID: fmt.Sprintf("cache-%s", rid),
		Choices: []models.Choice{{
			Message: models.Message{Role: "assistant", Content: cacheResp.Response},
		}},
	})
	observability.RequestsTotal.WithLabelValues("200_cache", model).Inc()
	return true
}

// runGuardrails 执行双阶段安全审计。
func (h *ChatHandler) runGuardrails(c *gin.Context, ctx context.Context, rid, prompt, model string) (string, bool) {
	nitroCtx, cancelNitro := context.WithTimeout(ctx, h.config.GuardrailNitroTimeout)
	defer cancelNitro()
	if rsResp, err := h.nitroClient.CheckInput(nitroCtx, &pb.InputRequest{Prompt: prompt}); err == nil {
		prompt = rsResp.SanitizedPrompt
	} else {
		slog.Warn("Nitro guardrail skip due to error", "request_id", rid, "error", err)
	}

	pyCtx, cancelPy := context.WithTimeout(ctx, h.config.GuardrailIntellTimeout)
	defer cancelPy()
	pyResp, err := h.intelligenceClient.CheckInput(pyCtx, &pb.InputRequest{Prompt: prompt})
	if err != nil {
		slog.Error("智能审计服务异常", "request_id", rid, "error", err)
		return prompt, true 
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
	pyCtx, cancelPy := context.WithTimeout(ctx, h.config.GuardrailIntellTimeout)
	defer cancelPy()

	pyResp, err := h.intelligenceClient.CheckOutput(pyCtx, &pb.OutputRequest{ResponseText: text})
	if err != nil {
		slog.Warn("输出审计服务不可用或超时，降级放行", "request_id", rid, "error", err)
		return text, true
	}

	if !pyResp.Safe {
		slog.Warn("输出安全拦截", "request_id", rid)
		return pyResp.SanitizedText, true 
	}

	return pyResp.SanitizedText, true
}

// streamExecute 执行流式转发逻辑。
func (h *ChatHandler) streamExecute(c *gin.Context, ctx context.Context, req *models.ChatCompletionRequest, rid string, start time.Time) {
	// 并发控制：获取令牌
	select {
	case h.semaphore <- struct{}{}:
		defer func() { <-h.semaphore }()
	case <-ctx.Done():
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "server_busy", "message": "并发请求达到阈值"})
		return
	}

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

	respCh, errCh := node.Adapter.ChatCompletionStream(req)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Flush()

	slog.Info("流式传输开始", "request_id", rid, "node", node.Name)
	flusher := c.Writer
	h.router.Tracker.RecordSuccess(node.Name, 0)

	firstTokenReceived := false
	var firstTokenTime time.Time
	streamStart := time.Now()
	chunkCount := 0
	fullResponse := ""

	// 流式滑动窗口：最多保留最近 100 个字符进行连续性敏感审查
	slidingWindow := ""
	maxWindowSize := 100
	blacklist := []string{"ignore previous", "forget all instructions", "system prompt", "大语言模型不能"}

	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errCh:
			if err != nil {
				fmt.Fprintf(c.Writer, "data: {\"error\": \"stream_error\", \"message\": \"%s\"}\n\n", err.Error())
				return
			}
		case streamResp, ok := <-respCh:
			if !ok {
				fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()
				
				// 计算并记录流式吞吐量 TPS (Chunk/s 作为简化估算)
				if firstTokenReceived {
					duration := time.Since(firstTokenTime).Seconds()
					if duration > 0.1 { // 避免分母过小导致极值
						tps := float64(chunkCount) / duration
						observability.TPS.WithLabelValues(req.Model, node.Name).Observe(tps)
					}
				}
				
				// 异步落盘流式完整内容
				if observability.GlobalAuditLogger != nil {
					observability.GlobalAuditLogger.Log(&observability.AuditRecord{
						Timestamp: time.Now(),
						RequestID: rid,
						APIKey:    c.GetString("api_key"),
						Model:     req.Model,
						Node:      node.Name,
						Prompt:    h.extractPrompt(req),
						Response:  fullResponse,
						Tokens:    chunkCount,
					})
				}
				
				return
			}

			// 记录 TTFT
			if !firstTokenReceived {
				firstTokenTime = time.Now()
				ttft := firstTokenTime.Sub(streamStart).Seconds()
				observability.TTFT.WithLabelValues(req.Model, node.Name).Observe(ttft)
				firstTokenReceived = true
			}

			// 统计 Chunk 以估算 Token 数量并进行滑动窗口审查
			if len(streamResp.Choices) > 0 && streamResp.Choices[0].Delta.Content != "" {
				chunkCount++
				content := streamResp.Choices[0].Delta.Content
				fullResponse += content
				slidingWindow += content
				if len(slidingWindow) > maxWindowSize {
					slidingWindow = slidingWindow[len(slidingWindow)-maxWindowSize:]
				}

				// 审查并实时阻断
				for _, badWord := range blacklist {
					if strings.Contains(strings.ToLower(slidingWindow), badWord) {
						slog.Warn("流式审查命中违规词汇，强行中断流", "request_id", rid, "bad_word", badWord)
						fmt.Fprintf(c.Writer, "data: {\"error\": \"moderation_triggered\", \"message\": \"[内容检测中止：触发流式安全防护]\"}\n\n")
						flusher.Flush()
						return // 强行退出 goroutine，关闭连接
					}
				}
			}

			data, _ := json.Marshal(streamResp)
			fmt.Fprintf(c.Writer, "data: %s\n\n", string(data))
			flusher.Flush()
		}
	}
}

// routeAndExecute 执行智能路由并调用最终的 AI 模型提供商。
func (h *ChatHandler) routeAndExecute(c *gin.Context, ctx context.Context, req *models.ChatCompletionRequest, rid string, start time.Time) {
	// 并发控制：获取令牌
	select {
	case h.semaphore <- struct{}{}:
		defer func() { <-h.semaphore }()
	case <-ctx.Done():
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "server_busy", "message": "并发请求达到阈值"})
		return
	}

	var excluded []string
	maxRetries := h.config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// 指数退避重试 (从第二次尝试开始)
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
			slog.Info("等待重试", "request_id", rid, "backoff_ms", backoff.Milliseconds())
			
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
		}

		routeCtx := &router.RouteContext{
			RequestID:    rid,
			Model:        req.Model,
			PromptTokens: len(h.extractPrompt(req)) / h.config.TokenEstimationFactor,
			UserTier:     c.GetString("key_label"),
			Headers:      map[string]string{"X-Route-Strategy": c.GetHeader("X-Route-Strategy")},
			ExcludeNodes: excluded,
		}

		node, err := h.router.Route(routeCtx)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "routing_error", "message": err.Error()})
			observability.RequestsTotal.WithLabelValues("503", req.Model).Inc()
			return
		}

		slog.Info("路由调用", "request_id", rid, "node", node.Name, "attempt", attempt)

		callStart := time.Now()
		resp, err := node.Adapter.ChatCompletion(req)
		callDuration := time.Since(callStart)

		if err != nil {
			h.router.Tracker.RecordFailure(node.Name)
			slog.Error("调用失败", "request_id", rid, "node", node.Name, "error", err)

			// 记录 Provider 维度错误指标 (3.2)
			errType := "other"
			errMsg := err.Error()
			if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "deadline") {
				errType = "timeout"
			} else if strings.Contains(errMsg, "429") || strings.Contains(errMsg, "rate") {
				errType = "rate_limit"
			}
			observability.ProviderErrors.WithLabelValues(node.Name, errType).Inc()

			if attempt < maxRetries {
				excluded = append(excluded, node.Name)
				continue
			}

			c.JSON(http.StatusBadGateway, gin.H{"error": "provider_error", "message": err.Error()})
			observability.RequestsTotal.WithLabelValues("502", req.Model).Inc()
			return
		}

		h.router.Tracker.RecordSuccess(node.Name, callDuration)

		// 记录配额消耗 (根据 Token 数更新 Redis)
		if apiKey := c.GetString("api_key"); apiKey != "" && resp.Usage.TotalTokens > 0 {
			go middleware.UpdateQuotaUsage(context.Background(), h.rdb, apiKey, int64(resp.Usage.TotalTokens))
		}

		if len(resp.Choices) > 0 {
			auditedText, _ := h.runOutputGuardrails(ctx, rid, resp.Choices[0].Message.Content, node.ModelID)
			resp.Choices[0].Message.Content = auditedText
		}

		duration := time.Since(start)
		observability.Latency.WithLabelValues(req.Model).Observe(duration.Seconds())
		observability.RequestsTotal.WithLabelValues("200", req.Model).Inc()

		slog.Info("请求完成", "request_id", rid, "duration_ms", duration.Milliseconds())
		
		// 异步合规审计落盘
		if observability.GlobalAuditLogger != nil {
			var respText string
			if len(resp.Choices) > 0 {
				respText = resp.Choices[0].Message.Content
			}
			observability.GlobalAuditLogger.Log(&observability.AuditRecord{
				Timestamp: time.Now(),
				RequestID: rid,
				APIKey:    c.GetString("api_key"),
				Model:     req.Model,
				Node:      node.Name,
				Prompt:    h.extractPrompt(req),
				Response:  respText,
				Tokens:    resp.Usage.TotalTokens,
			})
		}
		
		c.JSON(http.StatusOK, resp)
		return
	}
}
