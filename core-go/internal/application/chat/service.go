package chat

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/pipeline"
	"github.com/gin-gonic/gin"
)

type Service struct {
	flow      Flow
	config    *config.Config
	semaphore chan struct{}
}

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

func (s *Service) HandleChatCompletions(c *gin.Context) {
	start := time.Now()
	env, decision := s.flow.Normalize(c, start)
	if decision != nil {
		s.flow.RespondDecision(c, env, decision)
		return
	}

	ctx := observability.NewOutContext(c.Request.Context(), env.RequestID)
	ctx, cancel := context.WithTimeout(ctx, s.config.RequestTimeout)
	defer cancel()

	decision = s.flow.EvaluatePolicies(c, ctx, env)
	if !decision.Allow {
		s.flow.RespondDecision(c, env, decision)
		return
	}
	if decision.Degraded {
		s.flow.RecordDegraded(env, "", decision.DegradeReason)
	}

	plan, planDecision := s.flow.BuildPlan(ctx, c, env)
	if planDecision != nil && !planDecision.Allow {
		s.flow.RespondDecision(c, env, planDecision)
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
		s.flow.RespondDecision(c, env, execDecision)
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
				s.flow.RecordExecutionCompleted(env, nodeName, fullResponseBuilder.String(), chunkCount, result.Degraded, joinDegradeReasons(result.DegradeReason, "stream provider error"), "stream_error")
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
				s.flow.RecordExecutionCompleted(env, nodeName, fullResponseBuilder.String(), chunkCount, result.Degraded, result.DegradeReason, "stream_completed")
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
					s.flow.RecordStreamBlocked(env, nodeName, fullResponseBuilder.String(), moderationDecision.Reason, result.Degraded, result.DegradeReason, chunkCount)
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
		s.flow.RespondDecision(c, env, execDecision)
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
	s.flow.RecordExecutionCompleted(env, nodeName, responseText, result.Response.Usage.TotalTokens, result.Degraded, result.DegradeReason, statusLabel)

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
