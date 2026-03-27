package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/nitro"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/pipeline"
	"github.com/ai-gateway/core/internal/router"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// ChatHandler 只负责驱动统一 pipeline，并处理 HTTP/SSE 协议细节。
// 具体的策略判断和执行计划由 pipeline 层统一完成。
type ChatHandler struct {
	pipeline  *pipeline.ChatPipeline
	config    *config.Config
	semaphore chan struct{}
}

// NewChatHandler 创建聊天入口处理器，并初始化用于保护上游的并发信号量。
func NewChatHandler(ic pb.AiLogicClient, nc nitro.NitroClient, sr *router.SmartRouter, rdb *redis.Client, cfg *config.Config) *ChatHandler {
	limit := cfg.MaxConcurrentRequests
	if limit <= 0 {
		limit = 1000
	}
	return &ChatHandler{
		pipeline:  pipeline.NewChatPipeline(ic, nc, sr, rdb, cfg),
		config:    cfg,
		semaphore: make(chan struct{}, limit),
	}
}

// HandleChatCompletions 是聊天请求的总入口：
// 解析请求、跑统一策略、生成执行计划，然后分发到同步或流式执行路径。
func (h *ChatHandler) HandleChatCompletions(c *gin.Context) {
	start := time.Now()
	env, decision := h.pipeline.Normalize(c, start)
	if decision != nil {
		h.pipeline.RespondDecision(c, env, decision)
		return
	}

	ctx := observability.NewOutContext(c.Request.Context(), env.RequestID)
	ctx, cancel := context.WithTimeout(ctx, h.config.RequestTimeout)
	defer cancel()

	decision = h.pipeline.EvaluatePolicies(c, ctx, env)
	if !decision.Allow {
		h.pipeline.RespondDecision(c, env, decision)
		return
	}
	if decision.Degraded {
		h.pipeline.RecordDegraded(env, "", decision.DegradeReason)
	}

	plan, planDecision := h.pipeline.BuildPlan(ctx, c, env)
	if planDecision != nil && !planDecision.Allow {
		h.pipeline.RespondDecision(c, env, planDecision)
		return
	}
	if planDecision != nil && planDecision.Degraded {
		h.pipeline.RecordDegraded(env, "", planDecision.DegradeReason)
		decision.Degraded = true
		decision.DegradeReason = joinDegradeReasons(decision.DegradeReason, planDecision.DegradeReason)
	}

	if env.Request.Stream {
		h.streamExecute(c, ctx, env, plan, decision)
		return
	}
	h.routeAndExecute(c, ctx, env, plan, decision)
}

// streamExecute 负责流式协议输出，本身不再做零散业务判断。
// 它只消费 pipeline 给出的执行结果，并补上 SSE 输出、TTFT/TPS 指标和流式审计。
func (h *ChatHandler) streamExecute(c *gin.Context, ctx context.Context, env *pipeline.RequestEnvelope, plan *pipeline.ExecutionPlan, decision *pipeline.PolicyDecision) {
	if !h.acquire(c, ctx) {
		return
	}
	defer h.release()

	result, execDecision := h.pipeline.ExecuteStream(env, plan, decision.Degraded, decision.DegradeReason)
	if execDecision != nil && !execDecision.Allow {
		h.pipeline.RespondDecision(c, env, execDecision)
		return
	}

	nodeName := ""
	if result.Node != nil {
		nodeName = result.Node.Name
	}
	if result.Degraded {
		h.pipeline.RecordDegraded(env, nodeName, result.DegradeReason)
	}
	h.pipeline.RecordExecutionStarted(env, nodeName, result.Degraded, result.DegradeReason)

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
				fmt.Fprintf(c.Writer, "data: {\"error\": \"stream_error\", \"message\": \"%s\"}\n\n", err.Error())
				flusher.Flush()
				h.pipeline.RecordExecutionCompleted(env, nodeName, fullResponseBuilder.String(), chunkCount, result.Degraded, joinDegradeReasons(result.DegradeReason, "stream provider error"), "stream_error")
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
				h.pipeline.RecordExecutionCompleted(env, nodeName, fullResponseBuilder.String(), chunkCount, result.Degraded, result.DegradeReason, "stream_completed")
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

				if moderationDecision := h.pipeline.GuardStreamChunk(&moderationWindow, content); moderationDecision != nil {
					fmt.Fprintf(c.Writer, "data: {\"error\": \"%s\", \"message\": \"%s\"}\n\n", moderationDecision.ErrorCode, moderationDecision.Message)
					flusher.Flush()
					h.pipeline.RecordStreamBlocked(env, nodeName, fullResponseBuilder.String(), moderationDecision.Reason, result.Degraded, result.DegradeReason, chunkCount)
					return
				}
			}

			fmt.Fprintf(c.Writer, "data: %s\n\n", string(pipeline.MarshalSSEData(streamResp)))
			flusher.Flush()
		}
	}
}

// routeAndExecute 负责非流式请求的最终输出。
func (h *ChatHandler) routeAndExecute(c *gin.Context, ctx context.Context, env *pipeline.RequestEnvelope, plan *pipeline.ExecutionPlan, decision *pipeline.PolicyDecision) {
	if !h.acquire(c, ctx) {
		return
	}
	defer h.release()

	result, execDecision := h.pipeline.ExecuteSync(ctx, env, plan, decision.Degraded, decision.DegradeReason)
	if execDecision != nil && !execDecision.Allow {
		h.pipeline.RespondDecision(c, env, execDecision)
		return
	}

	nodeName := ""
	if result.Node != nil {
		nodeName = result.Node.Name
	}
	if result.Degraded {
		h.pipeline.RecordDegraded(env, nodeName, result.DegradeReason)
	}
	h.pipeline.RecordExecutionStarted(env, nodeName, result.Degraded, result.DegradeReason)

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
	h.pipeline.RecordExecutionCompleted(env, nodeName, responseText, result.Response.Usage.TotalTokens, result.Degraded, result.DegradeReason, statusLabel)

	slog.Info("request completed", "request_id", env.RequestID, "duration_ms", duration.Milliseconds(), "node", nodeName, "cache_hit", result.FromCache)
	c.JSON(http.StatusOK, result.Response)
}

// acquire/release 用于在 handler 层保护上游并发，避免执行阶段把 provider 压垮。
func (h *ChatHandler) acquire(c *gin.Context, ctx context.Context) bool {
	select {
	case h.semaphore <- struct{}{}:
		return true
	case <-ctx.Done():
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "server_busy", "message": "concurrent request limit reached"})
		return false
	}
}

func (h *ChatHandler) release() {
	<-h.semaphore
}

func (h *ChatHandler) extractPrompt(req *models.ChatCompletionRequest) string {
	if req == nil || len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[len(req.Messages)-1].GetText()
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
