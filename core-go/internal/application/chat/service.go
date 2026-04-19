package chat

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/middleware"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/pipeline"
	"github.com/gin-gonic/gin"
)

// Service 是聊天补全业务的核心协调器（Orchestrator）。
// 它负责在 HTTP 传输层与复杂的 RequestFlow 引擎之间建立桥梁，处理并发控制、上下文生命周期管理以及最终响应的封装。
type Service struct {
	flow      Flow           // 业务流程引擎，负责具体的策略判定、路由与执行
	config    *config.Config // 系统级全局配置
	semaphore chan struct{}  // 并发控制信号量，防止过大的瞬时流量压垮后端
}

// NewService 构造 Service 实例。
// 它根据配置中的 MaxConcurrentRequests 初始化信号量。如果未配置，默认限制为 1000。
func NewService(flow Flow, cfg *config.Config) *Service {
	limit := cfg.MaxConcurrentRequests
	if limit <= 0 {
		limit = 1000
	}
	return &Service{
		flow:      flow,
		config:    cfg,
		semaphore: make(chan struct{}, limit),
	}
}

// HandleChatCompletions 是 OpenAI 兼容接口 /v1/chat/completions 的处理入口。
// 该方法体现了网关的“流水线”设计模式：
// 1. 协议标准化（Normalize）：将 rawBody 转为内部信封模型 Envelope。
// 2. 预置策略评估（EvaluatePolicies）：执行 Auth、RateLimit、Quota 等不涉及核心路由的策略。
// 3. 构建执行计划（BuildPlan）：根据路由规则决定使用哪个供应商物理节点，并处理潜在的降级方案。
// 4. 分支执行：根据是否是 Stream 请求进入不同的执行路径。
func (s *Service) HandleChatCompletions(c *gin.Context) {
	start := time.Now()

	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_payload", "message": "Failed to read request body"})
		return
	}

	var tenantID uint
	var apiKeyID uint
	if tID, exists := c.Get("tenant_id"); exists {
		if val, ok := tID.(uint); ok {
			tenantID = val
		}
	}
	if akID, exists := c.Get("api_key_id"); exists {
		if val, ok := akID.(uint); ok {
			apiKeyID = val
		}
	}

	meta := &pipeline.RequestMetadata{
		Headers: map[string]string{
			middleware.HeaderXRequestID: c.GetString(middleware.RequestIDKey),
			"X-Session-ID":              c.GetHeader("X-Session-ID"),
			"X-Route-Strategy":          c.GetHeader("X-Route-Strategy"),
			"X-Internal-API-Key":        c.GetString("api_key"),
			"X-Internal-Key-Label":      c.GetString("key_label"),
		},
		TenantID: tenantID,
		APIKeyID: apiKeyID,
	}

	env, decision := s.flow.Normalize(c.Request.Context(), rawBody, meta, start)
	if decision != nil {
		s.RespondDecision(c, env, decision)
		return
	}

	ctx := observability.NewOutContext(c.Request.Context(), env.RequestID)
	ctx, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()

	decision = s.flow.EvaluatePolicies(ctx, env)
	if !decision.Allow {
		s.RespondDecision(c, env, decision)
		return
	}
	if decision.Degraded {
		s.flow.RecordDegraded(env, "", decision.DegradeReason)
	}

	plan, planDecision := s.flow.BuildPlan(ctx, env, meta)
	if planDecision != nil && !planDecision.Allow {
		s.RespondDecision(c, env, planDecision)
		return
	}
	if planDecision != nil && planDecision.Degraded {
		s.flow.RecordDegraded(env, "", planDecision.DegradeReason)
		decision.Degraded = true
		decision.DegradeReason = joinDegradeReasons(decision.DegradeReason, planDecision.DegradeReason)
	}

	if env.Request.Stream {
		s.streamExecute(c, ctx, env, plan, decision)
		return
	}
	s.routeAndExecute(c, ctx, env, plan, decision)
}

func (s *Service) streamExecute(c *gin.Context, ctx context.Context, env *pipeline.RequestEnvelope, plan *pipeline.ExecutionPlan, decision *pipeline.PolicyDecision) {
	if !s.acquire(c, ctx) {
		return
	}
	defer s.release()

	result, execDecision := s.flow.ExecuteStream(ctx, env, plan, decision.Degraded, decision.DegradeReason)
	if execDecision != nil && !execDecision.Allow {
		s.RespondDecision(c, env, execDecision)
		return
	}

	nodeName := ""
	if result.Node != nil {
		nodeName = result.Node.Name
	}
	if result.Degraded {
		s.flow.RecordDegraded(env, nodeName, result.DegradeReason)
	}
	s.flow.RecordExecutionStarted(env, nodeName, result.Degraded, result.DegradeReason)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Flush()

	flusher := c.Writer

	firstTokenReceived := false
	streamStart := time.Now()
	var firstTokenTime time.Time
	chunkCount := 0
	var fullResponseBuilder strings.Builder
	var moderationWindow strings.Builder

	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-result.StreamErrors:
			if ok && err != nil {
				slog.Error("stream execution error", "error", err, "request_id", env.RequestID)
				fmt.Fprintf(c.Writer, "data: {\"error\": \"stream_error\", \"message\": \"An internal error occurred during streaming.\"}\n\n")
				flusher.Flush()
				promptTokens := len(env.Prompt) / 4 // Simple heuristic if not available
				s.flow.RecordExecutionCompleted(env, nodeName, fullResponseBuilder.String(), promptTokens, chunkCount, result.Degraded, joinDegradeReasons(result.DegradeReason, "stream provider error"), "stream_error")
				return
			}
		case streamResp, ok := <-result.Stream:
			if !ok {
				fmt.Fprintf(c.Writer, "data: [DONE]\n\n")
				flusher.Flush()

				if firstTokenReceived {
					duration := time.Since(firstTokenTime).Seconds()
					if duration > 0.1 {
						observability.TPS.WithLabelValues(env.Request.Model, nodeName).Observe(float64(chunkCount) / duration)
					}
				}
				promptTokens := len(env.Prompt) / 4
				s.flow.RecordExecutionCompleted(env, nodeName, fullResponseBuilder.String(), promptTokens, chunkCount, result.Degraded, result.DegradeReason, "stream_completed")
				s.flow.GuardOutputAsync(env.RequestID, env, nodeName, fullResponseBuilder.String())
				return
			}

			if !firstTokenReceived {
				firstTokenReceived = true
				firstTokenTime = time.Now()
				observability.TTFT.WithLabelValues(env.Request.Model, nodeName).Observe(firstTokenTime.Sub(streamStart).Seconds())
			}

			if len(streamResp.Choices) > 0 && streamResp.Choices[0].Delta.Content != "" {
				content := streamResp.Choices[0].Delta.Content
				chunkCount++
				fullResponseBuilder.WriteString(content)

				if moderationDecision := s.flow.GuardStreamChunk(&moderationWindow, content); moderationDecision != nil {
					fmt.Fprintf(c.Writer, "data: {\"error\": \"%s\", \"message\": \"%s\"}\n\n", moderationDecision.ErrorCode, moderationDecision.Message)
					flusher.Flush()
					promptTokens := len(env.Prompt) / 4
					s.flow.RecordStreamBlocked(env, nodeName, fullResponseBuilder.String(), moderationDecision.Reason, result.Degraded, result.DegradeReason, promptTokens, chunkCount)
					return
				}
			}

			fmt.Fprintf(c.Writer, "data: %s\n\n", string(pipeline.MarshalSSEData(streamResp)))
			flusher.Flush()
		}
	}
}

func (s *Service) routeAndExecute(c *gin.Context, ctx context.Context, env *pipeline.RequestEnvelope, plan *pipeline.ExecutionPlan, decision *pipeline.PolicyDecision) {
	if !s.acquire(c, ctx) {
		return
	}
	defer s.release()

	result, execDecision := s.flow.ExecuteSync(ctx, env, plan, decision.Degraded, decision.DegradeReason)
	if execDecision != nil && !execDecision.Allow {
		s.RespondDecision(c, env, execDecision)
		return
	}

	nodeName := ""
	if result.Node != nil {
		nodeName = result.Node.Name
	}
	if result.Degraded {
		s.flow.RecordDegraded(env, nodeName, result.DegradeReason)
	}
	s.flow.RecordExecutionStarted(env, nodeName, result.Degraded, result.DegradeReason)

	duration := time.Since(env.Start)
	observability.Latency.WithLabelValues(env.Request.Model).Observe(duration.Seconds())
	statusLabel := "200"
	if result.FromCache {
		statusLabel = "200_cache"
	}
	observability.RequestsTotal.WithLabelValues(statusLabel, env.Request.Model).Inc()

	responseText := ""
	if len(result.Response.Choices) > 0 {
		responseText = result.Response.Choices[0].Message.GetText()
	}
	s.flow.RecordExecutionCompleted(env, nodeName, responseText, result.Response.Usage.PromptTokens, result.Response.Usage.CompletionTokens, result.Degraded, result.DegradeReason, statusLabel)


	c.JSON(http.StatusOK, result.Response)
}

func (s *Service) acquire(c *gin.Context, ctx context.Context) bool {
	select {
	case s.semaphore <- struct{}{}:
		return true
	case <-ctx.Done():
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "server_busy", "message": "concurrent request limit reached"})
		return false
	}
}

func (s *Service) release() {
	<-s.semaphore
}

func joinDegradeReasons(parts ...string) string {
	var reasons []string
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		reasons = append(reasons, part)
	}
	return strings.Join(reasons, "; ")
}

// RespondDecision 统一处理返回给客户端的错误和拦截决策
func (s *Service) RespondDecision(c *gin.Context, env *pipeline.RequestEnvelope, decision *pipeline.PolicyDecision) {
	if decision == nil {
		return
	}
	if decision.Allow && decision.StatusCode == 0 {
		return
	}

	if decision.RetryAfter != "" {
		c.Header("Retry-After", decision.RetryAfter)
	}

	model := "unknown"
	if env != nil && env.Model != "" {
		model = env.Model
	}
	observability.RequestsTotal.WithLabelValues(strconv.Itoa(decision.StatusCode), model).Inc()

	payload := gin.H{"error": decision.ErrorCode}
	if decision.Message != "" {
		payload["message"] = decision.Message
	}
	if decision.Reason != "" {
		payload["reason"] = decision.Reason
	}
	if env != nil && env.RequestID != "" {
		payload["request_id"] = env.RequestID
	}

	s.flow.RecordRejected(env, decision)
	c.JSON(decision.StatusCode, payload)
}
