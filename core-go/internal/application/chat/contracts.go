package chat

import (
	"context"
	"strings"
	"time"

	"github.com/ai-gateway/core/internal/pipeline"
	"github.com/gin-gonic/gin"
)

type RequestNormalizer interface {
	Normalize(c *gin.Context, start time.Time) (*pipeline.RequestEnvelope, *pipeline.PolicyDecision)
}

type PolicyService interface {
	EvaluatePolicies(c *gin.Context, ctx context.Context, env *pipeline.RequestEnvelope) *pipeline.PolicyDecision
}

type Planner interface {
	BuildPlan(ctx context.Context, c *gin.Context, env *pipeline.RequestEnvelope) (*pipeline.ExecutionPlan, *pipeline.PolicyDecision)
}

type Executor interface {
	ExecuteSync(ctx context.Context, env *pipeline.RequestEnvelope, plan *pipeline.ExecutionPlan, degraded bool, degradeReason string) (*pipeline.ExecutionResult, *pipeline.PolicyDecision)
	ExecuteStream(env *pipeline.RequestEnvelope, plan *pipeline.ExecutionPlan, degraded bool, degradeReason string) (*pipeline.ExecutionResult, *pipeline.PolicyDecision)
	GuardStreamChunk(window *strings.Builder, chunk string) *pipeline.PolicyDecision
	GuardOutputAsync(rid string, env *pipeline.RequestEnvelope, nodeName string, fullText string)
}

type AuditSink interface {
	RespondDecision(c *gin.Context, env *pipeline.RequestEnvelope, decision *pipeline.PolicyDecision)
	RecordExecutionStarted(env *pipeline.RequestEnvelope, nodeName string, degraded bool, degradeReason string)
	RecordExecutionCompleted(env *pipeline.RequestEnvelope, nodeName string, responseText string, tokens int, degraded bool, degradeReason string, status string)
	RecordStreamBlocked(env *pipeline.RequestEnvelope, nodeName string, responseText string, reason string, degraded bool, degradeReason string, tokens int)
	RecordDegraded(env *pipeline.RequestEnvelope, nodeName string, reason string)
}

type Flow interface {
	RequestNormalizer
	PolicyService
	Planner
	Executor
	AuditSink
}
