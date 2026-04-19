package chat

import (
	"context"
	"strings"
	"time"

	"github.com/ai-gateway/core/internal/pipeline"
)

// RequestNormalizer 负责将原始 HTTP 请求体（Payload）解析并标准化为网关内部统一的请求信封模型。
type RequestNormalizer interface {
	// Normalize 执行标准化操作，并进行基础的协议校验（如 JSON 格式、必需字段等）。
	Normalize(ctx context.Context, rawBody []byte, meta *pipeline.RequestMetadata, start time.Time) (*pipeline.RequestEnvelope, *pipeline.PolicyDecision)
}

// PolicyService 封装了网关的策略执行逻辑。
type PolicyService interface {
	// EvaluatePolicies 在请求路由前评估各项策略（如身份鉴权、配额检查、黑名单过滤等）。
	EvaluatePolicies(ctx context.Context, env *pipeline.RequestEnvelope) *pipeline.PolicyDecision
}

// Planner 负责根据业务逻辑和路由策略，为请求构建最优的执行方案。
type Planner interface {
	// BuildPlan 根据请求元数据和当前后端节点的负载/质量，选定目标节点并生成降级策略。
	BuildPlan(ctx context.Context, env *pipeline.RequestEnvelope, meta *pipeline.RequestMetadata) (*pipeline.ExecutionPlan, *pipeline.PolicyDecision)
}

// Executor 负责具体的服务调用执行。
type Executor interface {
	// ExecuteSync 执行阻塞式的非流式请求。
	ExecuteSync(ctx context.Context, env *pipeline.RequestEnvelope, plan *pipeline.ExecutionPlan, degraded bool, degradeReason string) (*pipeline.ExecutionResult, *pipeline.PolicyDecision)
	// ExecuteStream 执行 SSE 流式请求。
	ExecuteStream(ctx context.Context, env *pipeline.RequestEnvelope, plan *pipeline.ExecutionPlan, degraded bool, degradeReason string) (*pipeline.ExecutionResult, *pipeline.PolicyDecision)
	// GuardStreamChunk 执行流式的异步安全审计（输出护栏）。
	GuardStreamChunk(window *strings.Builder, chunk string) *pipeline.PolicyDecision
	// GuardOutputAsync 在非流式响应结束后，异步提交完整响应进行离线安全分析。
	GuardOutputAsync(rid string, env *pipeline.RequestEnvelope, nodeName string, fullText string)
}

// AuditSink 定义了系统审计与可观测性的持久化接口。
type AuditSink interface {
	RecordExecutionStarted(env *pipeline.RequestEnvelope, nodeName string, degraded bool, degradeReason string)
	RecordExecutionCompleted(env *pipeline.RequestEnvelope, nodeName string, responseText string, inputTokens, outputTokens int, degraded bool, degradeReason string, status string)
	RecordStreamBlocked(env *pipeline.RequestEnvelope, nodeName string, responseText string, reason string, degraded bool, degradeReason string, inputTokens, outputTokens int)
	RecordDegraded(env *pipeline.RequestEnvelope, nodeName string, reason string)
	RecordRejected(env *pipeline.RequestEnvelope, decision *pipeline.PolicyDecision)
}

// Flow 接口是对上述五个关键职责的聚合。
// 它定义了一个完整的 AI 网关请求生命周期所必须具备的所有能力，是微服务拆分的核心约束。
type Flow interface {
	RequestNormalizer
	PolicyService
	Planner
	Executor
	AuditSink
}
