package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/adapters"
	"github.com/ai-gateway/core/internal/cache"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/db"
	"github.com/ai-gateway/core/internal/dependencies"
	"github.com/ai-gateway/core/internal/middleware"
	"github.com/ai-gateway/core/internal/nitro"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/quota"
	"github.com/ai-gateway/core/internal/router"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/redis/go-redis/v9"
)

const (
	EventReceived   = "request_received"
	EventRejected   = "request_rejected"
	EventStarted    = "execution_started"
	EventCompleted  = "execution_completed"
	EventStreamStop = "stream_blocked"
	EventDegraded   = "degraded"
)

var streamModerationBlacklist = []string{
	"ignore previous",
	"forget all instructions",
	"system prompt",
	"large language model cannot",
}

// RequestEnvelope 承载单次 Chat 请求在 Pipeline 流转中的所有上下文信息。
// 它将原始请求、用户信息、租户信息以及计算出的 Prompt 聚合在一起，是整个流水线的核心数据载体。
type RequestEnvelope struct {
	RequestID string                        // 唯一请求 ID
	APIKey    string                        // 原始 API Key
	KeyLabel  string                        // API Key 的标签（用于限流和计费）
	SessionID string                        // 会话 ID（用于上下文存储）
	Model     string                        // 目标模型名称
	Prompt    string                        // 经过提取的最后一条提问文本
	Start     time.Time                     // 请求到达网关的开始时间
	Request   *models.ChatCompletionRequest // 原始 OpenAI 格式请求对象
	TenantID  uint                          // 所属租户 ID
	APIKeyID  uint                          // API Key 的数据库主键 ID
}

// PolicyDecision 描述策略引擎对请求的处理决策。
// 包括是否允许通过、拦截的状态码、拦截原因，以及在通过时可能附带的脱敏 Prompt 或降级标记。
type PolicyDecision struct {
	Allow           bool            // 是否允许请求继续流转
	StatusCode      int             // 拦截时的 HTTP 状态码
	ErrorCode       string          // 拦截时的业务错误码
	Message         string          // 返回给客户端的友好提示信息
	Reason          string          // 内部审计用的详细拦截原因
	SanitizedPrompt string          // 经过内容安全审计（如脱敏）后的 Prompt
	RetryAfter      string          // 限流时建议的重试秒数
	Degraded        bool            // 是否处于降级模式运行
	DegradeReason   string          // 降级的具体原因（如缓存失效、安全服务不可用等）
}

// ExecutionPlan 定义了经过路由决策后的执行计划。
// 包含路由上下文、选中的下游节点（适配器）以及预提取的缓存命中结果（如有）。
type ExecutionPlan struct {
	RouteContext *router.RouteContext           // 路由计算上下文
	Node         *router.ModelNode              // 选中的物理执行节点
	Cached       *models.ChatCompletionResponse // 缓存结果（若命中则不调用下游）
}

// ExecutionResult 描述执行阶段产生的最终结果。
// 支持同步响应对象和异步流式通道。
type ExecutionResult struct {
	Response      *models.ChatCompletionResponse              // 同步执行的响应内容
	Stream        <-chan *models.ChatCompletionStreamResponse // 流式执行的数据通道
	StreamErrors  <-chan error                                // 流式执行的错误通道
	Node          *router.ModelNode                           // 最终承载请求的节点信息
	FromCache     bool                                        // 标记结果是否来自语义缓存
	Degraded      bool                                        // 执行过程是否应用了降级逻辑
	DegradeReason string                                      // 执行阶段的降级说明
}

// ChatPipeline 是网关处理对话请求的核心流水线。
// 它负责协调策略评估、路由选择、下游执行以及执行后的安全审计与统计。
type ChatPipeline struct {
	policyEngine *PolicyEngine        // 基于规则或 DSL 的策略引擎
	dependencies *dependencies.Facade // 外部依赖外壳（包含安全、计费等服务）
	router       *router.SmartRouter  // 智能调度路由器
	config       *config.Config       // 系统全局配置
	rdb          *redis.Client        // Redis 客户端，用于限流与配额
	contextStore *cache.ContextStore  // 持久化会话存储
	retryBudget  *RetryBudgetPolicy   // 重试预算管理器，防止级联故障
	costEngine   db.CostEngine        // 计费统计引擎
	now          func() time.Time     // 时间函数（主要用于 Mock 测试）

	// 语义缓存相关组件
	vectorStore       cache.VectorStore          // 向量特征库
	embeddingProvider adapters.EmbeddingProvider // 文本转向量服务（如 OpenAI Embeddings）
}

// NewChatPipeline 创建并初始化一个新的对话流水线实例。
func NewChatPipeline(ic pb.AiLogicClient, nc nitro.NitroClient, sr *router.SmartRouter, tm db.TenantManager, ce db.CostEngine, rdb *redis.Client, cfg *config.Config) *ChatPipeline {
	// 注册所有已实现的内置策略（如限流、指纹验证等）
	RegisterPolicies()

	facade := dependencies.NewFacade(ic, nc, cfg)
	deps := &DependencyContainer{
		Dependencies:  facade,
		RedisClient:   rdb,
		Config:        cfg,
		TenantManager: tm,
		CostEngine:    ce,
	}

	// 初始化策略引擎，加载 declarative 策略定义的 YAML 文件
	policyPath := resolvePolicyPath(cfg)
	engine, err := NewPolicyEngine(policyPath, deps)
	if err != nil {
		slog.Error("failed to load policy engine, requests may be blocked", "path", policyPath, "error", err)
	}

	// 根据配置初始化语义缓存组件。如果未配置 OpenAI Key，则不启用本地语义缓存功能
	var vectorStore cache.VectorStore
	var embeddingProvider adapters.EmbeddingProvider

	if cfg.OpenAIApiKey != "" {
		vectorStore = cache.NewMemoryVectorStore()
		embeddingProvider = adapters.NewOpenAIEmbeddingProvider(cfg.OpenAIApiKey, cfg.OpenAIURL, "text-embedding-3-small")
	}

	return &ChatPipeline{
		policyEngine:      engine,
		dependencies:      facade,
		router:            sr,
		config:            cfg,
		rdb:               rdb,
		contextStore:      cache.NewContextStore(rdb, 24*time.Hour),
		retryBudget:       NewRetryBudgetPolicy(100.0, 50),
		costEngine:        ce,
		now:               time.Now,
		vectorStore:       vectorStore,
		embeddingProvider: embeddingProvider,
	}
}

// Normalize 负责将原始 HTTP 请求体解析并标准化为网关内部通用的 RequestEnvelope。
// 这一步还包括提取 API Key、Session ID 以及初步的元数据补全。
func (p *ChatPipeline) Normalize(ctx context.Context, rawBody []byte, meta *RequestMetadata, start time.Time) (*RequestEnvelope, *PolicyDecision) {
	var req models.ChatCompletionRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		return nil, &PolicyDecision{
			StatusCode: http.StatusBadRequest,
			ErrorCode:  "invalid_payload",
			Message:    err.Error(),
			Reason:     "请求体 JSON 解析失败",
		}
	}

	sessionID := ""
	if meta != nil && meta.Headers != nil {
		sessionID = meta.Headers["X-Session-ID"]
	}

	rid := ""
	if meta != nil && meta.Headers != nil {
		rid = meta.Headers[middleware.HeaderXRequestID]
	}

	apiKey := ""
	keyLabel := "anonymous"
	if meta != nil && meta.Headers != nil {
		if val, ok := meta.Headers["X-Internal-API-Key"]; ok {
			apiKey = val
		}
		if val, ok := meta.Headers["X-Internal-Key-Label"]; ok {
			keyLabel = val
		}
	}

	var tenantID uint
	var apiKeyID uint
	if meta != nil {
		tenantID = meta.TenantID
		apiKeyID = meta.APIKeyID
	}

	env := &RequestEnvelope{
		RequestID: rid,
		APIKey:    apiKey,
		KeyLabel:  keyLabel,
		SessionID: sessionID,
		Model:     req.Model,
		Prompt:    extractPrompt(&req),
		Start:     start,
		Request:   &req,
		TenantID:  tenantID,
		APIKeyID:  apiKeyID,
	}
	if env.KeyLabel == "" {
		env.KeyLabel = "anonymous"
	}
	return env, nil
}

// EvaluatePolicies 执行 Pipeline 的前置策略评估。
// 它会调用 PolicyEngine 运行所有配置的策略（如内容安全审计、指纹校验等）。
func (p *ChatPipeline) EvaluatePolicies(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	if p.policyEngine == nil {
		return &PolicyDecision{
			StatusCode: http.StatusServiceUnavailable,
			ErrorCode:  "engine_unavailable",
			Message:    "策略引擎未就绪",
			Reason:     "策略中心加载失败",
		}
	}

	decision := p.policyEngine.Evaluate(ctx, env)
	if !decision.Allow {
		return decision
	}

	// 如果策略对 Prompt 进行了修改（如脱敏），则更新请求中的内容
	replaceLastMessage(env.Request, decision.SanitizedPrompt)
	return decision
}

// BuildPlan 根据当前请求上下文构建执行计划。
// 该步骤包含：历史对话注入、上下文多级缓存检索、以及最终通过 SmartRouter 选定物理节点。
func (p *ChatPipeline) BuildPlan(ctx context.Context, env *RequestEnvelope, meta *RequestMetadata) (*ExecutionPlan, *PolicyDecision) {
	// 异步统计 Token（用于监控，不阻塞主流程）
	p.asyncCountTokens(env.RequestID, env.Prompt, env.Model)

	// 1. 如果 SessionID 存在，则从 ContextStore 注入历史对话内容
	if env.SessionID != "" && p.contextStore != nil {
		history, err := p.contextStore.Retrieve(ctx, env.SessionID)
		if err == nil && len(history) > 0 {
			// 仅注入最近 10 轮对话以平衡消耗与性能
			if len(history) > 10 {
				history = history[len(history)-10:]
			}
			env.Request.Messages = append(history, env.Request.Messages...)
			slog.Debug("session context injected", "session_id", env.SessionID, "history_len", len(history))
		}
	}

	// 2. 自动裁剪上下文窗口，确保不溢出模型限制
	p.pruneMessages(ctx, env)

	// 3. 语义缓存检索（仅限非流式请求）
	if !env.Request.Stream {
		if cached, degraded, degradeReason, hardFailure := p.checkCache(ctx, env); cached != nil {
			return &ExecutionPlan{Cached: cached}, &PolicyDecision{Allow: true, Degraded: degraded, DegradeReason: degradeReason}
		} else if hardFailure {
			return nil, &PolicyDecision{
				StatusCode: http.StatusServiceUnavailable,
				ErrorCode:  "cache_error",
				Message:    "语义缓存服务暂时不可用",
				Reason:     degradeReason,
			}
		} else if degradeReason != "" || degraded {
			// 即使缓存检查失败也可以选择降级通过
			if meta != nil && meta.Headers != nil {
				meta.Headers["X-Internal-Cache-Degraded"] = "true"
				meta.Headers["X-Internal-Cache-Degrade-Reason"] = degradeReason
			}
		}
	}

	// 4. 调用 SmartRouter 进行动态路由决策
	if p.router == nil {
		return nil, &PolicyDecision{
			StatusCode: http.StatusServiceUnavailable,
			ErrorCode:  "routing_error",
			Message:    "路由器未配置",
			Reason:     "SmartRouter 未初始化",
		}
	}

	strategy := ""
	if meta != nil && meta.Headers != nil {
		strategy = meta.Headers["X-Route-Strategy"]
	}

	routeCtx := &router.RouteContext{
		RequestID:    env.RequestID,
		Model:        env.Request.Model,
		PromptTokens: len(env.Prompt) / maxInt(p.config.TokenEstimationFactor, 1),
		UserTier:     env.KeyLabel,
		Headers:      map[string]string{"X-Route-Strategy": strategy},
	}

	node, err := p.router.Route(routeCtx)
	if err != nil {
		return nil, &PolicyDecision{
			StatusCode: http.StatusServiceUnavailable,
			ErrorCode:  "routing_error",
			Message:    err.Error(),
			Reason:     "无可用的下游执行节点",
		}
	}

	return &ExecutionPlan{RouteContext: routeCtx, Node: node}, nil
}

// ExecuteSync 执行同步的对话请求。
// 该方法包含完整的错误重试逻辑：
// 1. 如果首次尝试失败，会根据配置的 MaxRetries 进行重试。
// 2. 每次重试前会消耗 RetryBudget（请求预算），防止对下游造成雪崩。
// 3. 失败后会排除当前故障节点并重新路由。
func (p *ChatPipeline) ExecuteSync(ctx context.Context, env *RequestEnvelope, plan *ExecutionPlan, degraded bool, degradeReason string) (*ExecutionResult, *PolicyDecision) {
	// 如果 BuildPlan 阶段已命中缓存，直接返回结果
	if plan.Cached != nil {
		response, outputDecision := p.guardOutputResponse(ctx, env, nil, plan.Cached)
		if outputDecision != nil && !outputDecision.Allow {
			return nil, outputDecision
		}
		return &ExecutionResult{
			Response:      response,
			FromCache:     true,
			Degraded:      degraded || (outputDecision != nil && outputDecision.Degraded),
			DegradeReason: joinReasons(degradeReason, decisionDegradeReason(outputDecision)),
		}, nil
	}

	var excluded []string
	maxRetries := p.config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	combinedDegraded := degraded
	combinedReason := degradeReason

	// 核心执行循环：支持多轮重试
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 检查重试预算，防止过度刷新下游导致崩溃
			if p.retryBudget != nil && !p.retryBudget.AcquireRetry() {
				slog.Warn("global retry budget exhausted", "request_id", env.RequestID, "attempt", attempt)
				return nil, &PolicyDecision{StatusCode: http.StatusTooManyRequests, ErrorCode: "retry_exhausted", Message: "当前系统繁忙，已触发过载保护", Reason: "重试预算耗尽"}
			}
			// 指数退避重试，减少下游压力
			backoff := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, &PolicyDecision{StatusCode: http.StatusRequestTimeout, ErrorCode: "request_cancelled", Message: "请求已被客户端取消", Reason: "执行上下文已取消"}
			}
		}

		// 重新路由（排除已知的故障节点）
		routeCtx := *plan.RouteContext
		routeCtx.ExcludeNodes = excluded
		node, err := p.router.Route(&routeCtx)
		if err != nil {
			return nil, &PolicyDecision{StatusCode: http.StatusServiceUnavailable, ErrorCode: "routing_error", Message: err.Error(), Reason: "无可用的备选目标"}
		}

		callStart := p.now()
		// 调用 Provider 适配器进行实际请求
		resp, err := node.Adapter.ChatCompletion(ctx, env.Request)
		callDuration := p.now().Sub(callStart)
		if err != nil {
			// 记录失败并将其加入排除列表，以便重试时选择其他节点
			p.router.Tracker.RecordFailure(node.Name)
			recordProviderError(node.Name, err)
			if attempt < maxRetries {
				excluded = append(excluded, node.Name)
				continue
			}
			return nil, &PolicyDecision{StatusCode: http.StatusBadGateway, ErrorCode: "provider_error", Message: err.Error(), Reason: "所有配置的 Provider 均调用失败"}
		}

		p.router.Tracker.RecordSuccess(node.Name, callDuration)
		
		// 响应侧安全审计
		response, outputDecision := p.guardOutputResponse(ctx, env, node, resp)
		if outputDecision != nil && !outputDecision.Allow {
			return nil, outputDecision
		}
		if outputDecision != nil && outputDecision.Degraded {
			combinedDegraded = true
			combinedReason = joinReasons(combinedReason, outputDecision.DegradeReason)
		}

		// 异步更新计费配额（Token 使用量）
		if env.APIKey != "" && response.Usage.TotalTokens > 0 && p.rdb != nil {
			go quota.UpdateUsage(context.Background(), p.rdb, env.APIKey, int64(response.Usage.TotalTokens))
		}

		// 异步更新语义缓存，用于后续相似请求的加速
		if p.embeddingProvider != nil && p.vectorStore != nil {
			responseText := response.Choices[0].Message.GetText()
			go func() {
				vec, err := p.embeddingProvider.Embed(context.Background(), env.Prompt)
				if err == nil {
					_ = p.vectorStore.Save(context.Background(), cache.VectorEntry{
						ID:     env.RequestID,
						Vector: vec,
						Data:   responseText,
					})
				}
			}()
		}

		return &ExecutionResult{Response: response, Node: node, FromCache: false, Degraded: combinedDegraded, DegradeReason: combinedReason}, nil
	}

	return nil, &PolicyDecision{StatusCode: http.StatusBadGateway, ErrorCode: "provider_error", Message: "Provider 执行异常", Reason: "无法达到执行终态"}
}

// ExecuteStream 执行流式对话请求。
// 目前流式请求不支持重试和缓存命中，以确保存储的一致性与低延迟。
func (p *ChatPipeline) ExecuteStream(ctx context.Context, env *RequestEnvelope, plan *ExecutionPlan, degraded bool, degradeReason string) (*ExecutionResult, *PolicyDecision) {
	if plan.Cached != nil {
		return nil, &PolicyDecision{StatusCode: http.StatusBadRequest, ErrorCode: "stream_cache_misshaped", Message: "流式请求暂不支持语义缓存直接回复", Reason: "SSE 渲染需要真实的流式驱动", Degraded: degraded}
	}
	// 直接获取流式通道
	stream, errCh := plan.Node.Adapter.ChatCompletionStream(ctx, env.Request)
	return &ExecutionResult{Stream: stream, StreamErrors: errCh, Node: plan.Node, Degraded: degraded, DegradeReason: degradeReason}, nil
}

// GuardStreamChunk 对流式传输的 Chunk 进行实时内容审计。
// 它维护一个滑动窗口以识别跨 Chunk 的违规关键词（如：攻击性的提示词注入指令）。
func (p *ChatPipeline) GuardStreamChunk(window *strings.Builder, chunk string) *PolicyDecision {
	window.WriteString(chunk)
	text := window.String()
	runes := []rune(text)
	// 窗口大小限制在最近 100 个字符，兼顾性能与跨 Chunk 语义识别
	if len(runes) > 100 {
		text = string(runes[len(runes)-100:])
		window.Reset()
		window.WriteString(text)
	}

	lower := strings.ToLower(text)
	for _, badWord := range streamModerationBlacklist {
		if strings.Contains(lower, badWord) {
			return &PolicyDecision{StatusCode: http.StatusForbidden, ErrorCode: "moderation_triggered", Message: "[内容已被拦截：触发流式安全策略]", Reason: badWord}
		}
	}
	return nil
}

// RespondDecision is removed from pipeline and should be handled by the transport layer.

func (p *ChatPipeline) RecordExecutionStarted(env *RequestEnvelope, nodeName string, degraded bool, degradeReason string) {
	p.logAudit(env, nodeNamePtr(nodeName), EventStarted, "accepted", "", "", degraded, degradeReason, 0)
}

func (p *ChatPipeline) RecordExecutionCompleted(env *RequestEnvelope, nodeName string, responseText string, inputTokens, outputTokens int, degraded bool, degradeReason string, status string) {
	tokens := inputTokens + outputTokens
	p.logAudit(env, nodeNamePtr(nodeName), EventCompleted, status, "", responseText, degraded, degradeReason, tokens)

	// 后台落盘 UsageLog
	if p.costEngine != nil && env != nil && status == "200" {
		go func() {
			usage := &db.UsageLog{
				RequestID:    env.RequestID,
				TenantID:     env.TenantID,
				APIKeyID:     env.APIKeyID,
				Model:        env.Model,
				Provider:     nodeName,
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
				CreatedAt:    p.now(),
			}
			_ = p.costEngine.RecordUsage(usage)
		}()
	}

	if env != nil && env.SessionID != "" && p.contextStore != nil && status == "200" && responseText != "" {
		go func() {
			currentRound := []models.Message{{Role: "user", Content: env.Prompt}, {Role: "assistant", Content: responseText}}
			_ = p.contextStore.Append(context.Background(), env.SessionID, currentRound)
		}()
	}
}

func (p *ChatPipeline) RecordStreamBlocked(env *RequestEnvelope, nodeName string, responseText string, reason string, degraded bool, degradeReason string, inputTokens, outputTokens int) {
	tokens := inputTokens + outputTokens
	p.logAudit(env, nodeNamePtr(nodeName), EventStreamStop, "blocked", reason, responseText, degraded, degradeReason, tokens)
}

func (p *ChatPipeline) RecordDegraded(env *RequestEnvelope, nodeName string, reason string) {
	observability.RecordDegradedEvent("pipeline", reason)
	p.logAudit(env, nodeNamePtr(nodeName), EventDegraded, "accepted", reason, "", true, reason, 0)
}

// RecordRejected 记录请求被拦截（审计失败或限流）事件。
func (p *ChatPipeline) RecordRejected(env *RequestEnvelope, decision *PolicyDecision) {
	if decision == nil {
		return
	}
	p.logAudit(env, nil, EventRejected, strconv.Itoa(decision.StatusCode), decision.Reason, "", decision.Degraded, decision.DegradeReason, 0)
}

// GuardOutputAsync 异步执行输出审计。
// 针对流式响应，我们采取“先发后审”的策略：先将 Chunk 推送给用户以保证响应速度，
// 同时在后台对累计文本进行异步审计。若发现违规，将记录审计异常。
func (p *ChatPipeline) GuardOutputAsync(rid string, env *RequestEnvelope, nodeName string, fullText string) {
	if p.dependencies == nil || p.dependencies.IntelligenceClient() == nil || fullText == "" {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), p.config.GuardrailIntellTimeout)
		defer cancel()

		resp, err := p.dependencies.IntelligenceClient().CheckOutput(ctx, &pb.OutputRequest{ResponseText: fullText})
		if err != nil {
			slog.Warn("async output guardrail check failed", "request_id", rid, "error", err)
			return
		}
		if !resp.Safe {
			slog.Warn("stream content violation detected post-delivery", "request_id", rid)
			p.logAudit(env, &nodeName, EventStreamStop, "violation", "流式输出中检测到不安全内容", fullText, true, "异步审计未通过", 0)
		}
	}()
}

// guardOutputResponse 执行同步输出审计。
// 在同步响应模式下，在将最终结果返回给用户前，必须通过安全审计服务（或 Nitro WASM 护栏）。
func (p *ChatPipeline) guardOutputResponse(ctx context.Context, env *RequestEnvelope, node *router.ModelNode, resp *models.ChatCompletionResponse) (*models.ChatCompletionResponse, *PolicyDecision) {
	if resp == nil || len(resp.Choices) == 0 || p.dependencies == nil {
		return resp, nil
	}

	outcome := p.dependencies.CheckOutput(ctx, resp.Choices[0].Message.GetText())
	if !outcome.Allowed {
		return resp, &PolicyDecision{StatusCode: outcome.StatusCode, ErrorCode: outcome.ErrorCode, Message: outcome.Message, Reason: outcome.Reason}
	}

	// 更新脱敏后的文本内容
	resp.Choices[0].Message.Content = outcome.SanitizedText
	if outcome.Degraded {
		return resp, &PolicyDecision{Allow: true, Degraded: true, DegradeReason: outcome.DegradeReason}
	}
	return resp, nil
}

// checkCache 执行两级语义缓存检索：
// 1. 本地级：使用 OpenAI Embedding + 内存向量库，速度最快。
// 2. 远程级：向后端 Intelligence 服务 (Python/Nitro gRPC) 查询更复杂的向量匹配。
func (p *ChatPipeline) checkCache(ctx context.Context, env *RequestEnvelope) (*models.ChatCompletionResponse, bool, string, bool) {
	// 1. 优先尝试本地语义缓存 (Go-native)
	if p.embeddingProvider != nil && p.vectorStore != nil {
		vec, err := p.embeddingProvider.Embed(ctx, env.Prompt)
		if err == nil {
			// 相似度阈值暂定为 0.9，匹配成功即直接返回缓存结果
			entry, _ := p.vectorStore.Search(ctx, vec, 0.9)
			if entry != nil {
				observability.CacheHitsTotal.WithLabelValues("hit_local", env.Model).Inc()
				return &models.ChatCompletionResponse{
					ID:    fmt.Sprintf("cache-local-%s", env.RequestID),
					Model: env.Model,
					Choices: []models.Choice{{
						Index:        0,
						Message:      models.Message{Role: "assistant", Content: entry.Data},
						FinishReason: "stop",
					}},
				}, false, "", false
			}
		} else {
			slog.Warn("local embedding failed for cache lookup", "request_id", env.RequestID, "error", err)
		}
	}

	// 2. 回退到依赖层 (Nitro gRPC / Python Side)
	if p.dependencies == nil {
		return nil, false, "", false
	}

	cacheOutcome := p.dependencies.GetCache(ctx, env.Prompt, env.Model)
	if cacheOutcome.HardFailure {
		observability.CacheHitsTotal.WithLabelValues("miss", env.Model).Inc()
		return nil, false, cacheOutcome.DegradeReason, true
	}
	if !cacheOutcome.Hit {
		observability.CacheHitsTotal.WithLabelValues("miss", env.Model).Inc()
		return nil, cacheOutcome.Degraded, cacheOutcome.DegradeReason, false
	}

	observability.CacheHitsTotal.WithLabelValues("hit_nitro", env.Model).Inc()
	return &models.ChatCompletionResponse{
		ID:    fmt.Sprintf("cache-%s", env.RequestID),
		Model: env.Model,
		Choices: []models.Choice{{
			Index:        0,
			Message:      models.Message{Role: "assistant", Content: cacheOutcome.Response},
			FinishReason: "stop",
		}},
	}, false, "", false
}

// pruneMessages 执行对话上下文的智能裁剪。
// 该方法会根据目标模型的 Token 限制（TokenEstimationFactor）计算当前消息序列的总长度。
// 如果总长度超过安全阈值，则从最早的消息开始舍弃，但在逻辑上保留 System Prompt 不被删除，以维持模型行为的一致性。
func (p *ChatPipeline) pruneMessages(ctx context.Context, env *RequestEnvelope) {
	// TODO: 实现更精准的 Tiktoken 计算逻辑
}

// recordProviderError 将底层网络或协议错误归类并记录到 Prometheus。
func recordProviderError(nodeName string, err error) {
	errType := "other"
	errMsg := strings.ToLower(err.Error())
	if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "deadline") {
		errType = "timeout"
	} else if strings.Contains(errMsg, "429") || strings.Contains(errMsg, "rate") {
		errType = "rate_limit"
	}
	observability.ProviderErrors.WithLabelValues(nodeName, errType).Inc()
}

// retryAfterSeconds 计算限流后的建议重试时间（秒）。
func retryAfterSeconds(qps float64) string {
	if qps <= 0 {
		return "1"
	}
	seconds := int(math.Ceil(1 / qps))
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}

// joinReasons 是一个辅助函数，用于将多个拦截或降级原因合并为一个紧凑的字符串，便于日志审计。
func joinReasons(parts ...string) string {
	var compact []string
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		compact = append(compact, part)
	}
	return strings.Join(compact, "; ")
}

func decisionDegradeReason(decision *PolicyDecision) string {
	if decision == nil {
		return ""
	}
	return decision.DegradeReason
}

func nodeNamePtr(name string) *string {
	if name == "" {
		return nil
	}
	return &name
}

// maxInt 返回两个整数中的较大值。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// resolvePolicyPath 探测并返回策略配置文件的路径。
// 它优先使用显式配置路径，若无，则在当前工作目录的常见路径（configs/policies.yaml）中进行查找。
func resolvePolicyPath(cfg *config.Config) string {
	if cfg != nil && cfg.Paths.PolicyFile != "" {
		return cfg.Paths.PolicyFile
	}

	wd, err := os.Getwd()
	if err != nil {
		return filepath.Join("configs", "policies.yaml")
	}

	candidates := []string{
		filepath.Join("configs", "policies.yaml"),
		filepath.Join("core-go", "configs", "policies.yaml"),
	}

	for _, c := range candidates {
		full := filepath.Join(wd, c)
		if _, err := os.Stat(full); err == nil {
			return full
		}
	}
	return filepath.Join("configs", "policies.yaml")
}

func (p *ChatPipeline) asyncCountTokens(rid, text, model string) {
	if p.dependencies == nil {
		return
	}

	go func() {
		tCtx, cancel := context.WithTimeout(context.Background(), p.config.TokenCountTimeout)
		defer cancel()
		tCtx = observability.NewOutContext(tCtx, rid)
		count, err := p.dependencies.CountTokens(tCtx, model, text)
		if err == nil && count > 0 {
			observability.TokenUsage.WithLabelValues(model).Add(float64(count))
		}
	}()
}

// logAudit 统一审计日志出口。
// 负责对敏感数据（如 API Key）进行脱敏，并对过长的 Prompt 进行截断后再录入持久化日志。
func (p *ChatPipeline) logAudit(env *RequestEnvelope, nodeName *string, event string, status string, reason string, response string, degraded bool, degradeReason string, tokens int) {
	if observability.GlobalAuditLogger == nil {
		return
	}
	record := &observability.AuditRecord{Timestamp: p.now(), Event: event, Status: status, Reason: joinReasons(reason, degradeReason), Degraded: degraded, Response: response, Tokens: tokens}
	if env != nil {
		record.RequestID = env.RequestID
		record.Model = env.Model

		// API Key 脱敏处理
		maskedKey := env.APIKey
		if len(maskedKey) > 10 {
			maskedKey = maskedKey[:6] + "***" + maskedKey[len(maskedKey)-4:]
		}
		record.APIKey = maskedKey

		// Prompt 截断，防止日志库因单条记录过大导致溢出
		truncatedPrompt := env.Prompt
		if len(truncatedPrompt) > 1000 {
			truncatedPrompt = truncatedPrompt[:1000] + "...(已截断)"
		}
		record.Prompt = truncatedPrompt
	}
	if nodeName != nil {
		record.Node = *nodeName
	}
	observability.GlobalAuditLogger.Log(record)
}

// extractPrompt 从请求消息序列中提取最后一条用户提问内容。
func extractPrompt(req *models.ChatCompletionRequest) string {
	if req == nil || len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[len(req.Messages)-1].GetText()
}

// replaceLastMessage 将请求中的最后一条消息替换为指定文本（通常用于内容脱敏后回写）。
func replaceLastMessage(req *models.ChatCompletionRequest, prompt string) {
	if req == nil || len(req.Messages) == 0 {
		return
	}
	last := &req.Messages[len(req.Messages)-1]
	switch v := last.Content.(type) {
	case string:
		last.Content = prompt
	case []models.ContentPart:
		for i, p := range v {
			if p.Type == "text" {
				v[i].Text = prompt
			}
		}
		last.Content = v
	case []interface{}:
		for _, p := range v {
			if m, ok := p.(map[string]interface{}); ok {
				if m["type"] == "text" {
					m["text"] = prompt
				}
			}
		}
	}
}

func checkRedisRateLimit(ctx context.Context, rdb *redis.Client, label string, qps float64, burst int) (bool, error) {
	key := "rl:" + label
	now := time.Now().UnixNano() / 1e6
	window := int64(1000)
	limit := int64(qps)
	if limit <= 0 {
		limit = 1
	}
	if burst > 0 && int64(burst) > limit {
		limit = int64(burst)
	}

	randSuffix := uuid.New().String()

	script := `
        local key = KEYS[1]
        local now = tonumber(ARGV[1])
        local window = tonumber(ARGV[2])
        local limit = tonumber(ARGV[3])
		local suffix = ARGV[4]

        redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
        local count = redis.call('ZCARD', key)
        if count >= limit then
            return 0
        else
            redis.call('ZADD', key, now, now .. ':' .. suffix)
            redis.call('PEXPIRE', key, window)
            return 1
        end
    `
	res, err := rdb.Eval(ctx, script, []string{key}, now, window, limit, randSuffix).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func recordProviderError(nodeName string, err error) {
	errType := "other"
	errMsg := strings.ToLower(err.Error())
	if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "deadline") {
		errType = "timeout"
	} else if strings.Contains(errMsg, "429") || strings.Contains(errMsg, "rate") {
		errType = "rate_limit"
	}
	observability.ProviderErrors.WithLabelValues(nodeName, errType).Inc()
}

func retryAfterSeconds(qps float64) string {
	if qps <= 0 {
		return "1"
	}
	seconds := int(math.Ceil(1 / qps))
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}

func joinReasons(parts ...string) string {
	var compact []string
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		compact = append(compact, part)
	}
	return strings.Join(compact, "; ")
}

func decisionDegradeReason(decision *PolicyDecision) string {
	if decision == nil {
		return ""
	}
	return decision.DegradeReason
}

func nodeNamePtr(name string) *string {
	if name == "" {
		return nil
	}
	return &name
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func resolvePolicyPath(cfg *config.Config) string {
	if cfg != nil && cfg.Paths.PolicyFile != "" {
		return cfg.Paths.PolicyFile
	}

	wd, err := os.Getwd()
	if err != nil {
		return filepath.Join("configs", "policies.yaml")
	}

	candidates := []string{
		filepath.Join("configs", "policies.yaml"),
		filepath.Join("core-go", "configs", "policies.yaml"),
	}

	dir := wd
	for range 8 {
		for _, candidate := range candidates {
			resolved := filepath.Join(dir, candidate)
			if _, statErr := os.Stat(resolved); statErr == nil {
				return resolved
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return filepath.Join("configs", "policies.yaml")
}

func MarshalSSEData(resp *models.ChatCompletionStreamResponse) []byte {
	data, err := json.Marshal(resp)
	if err != nil {
		slog.Error("failed to marshal SSE data", "error", err)
		return []byte(`{"error":"internal marshal error"}`)
	}
	return data
}
// pruneMessages 执行上下文窗口管理。
// 当消息历史过长超过设定的 Threshold (如 4000 Tokens) 时，会从早期对话开始裁剪，
// 以防止下游模型因 Context Window 超出上限而拒绝服务（500 错误）。
func (p *ChatPipeline) pruneMessages(ctx context.Context, env *RequestEnvelope) {
	if p.dependencies == nil {
		return
	}

	// 裁剪阈值。实际应用中应针对不同模型家族（GPT-4 vs Llama）动态调整。
	limit := 4000
	
	// 循环裁剪直到 Token 总数降至安全范围内。始终保留 System Prompt 和最新的一轮问答。
	for len(env.Request.Messages) > 2 {
		fullText := messagesToText(env.Request.Messages)
		count, err := p.dependencies.CountTokens(ctx, env.Model, fullText)
		if err != nil || count <= limit {
			break
		}

		// 移除索引为 1 (最早) 的历史对话对，直到满足窗口限制
		startIdx := 0
		if env.Request.Messages[0].Role == "system" {
			startIdx = 1
		}
		
		if len(env.Request.Messages) > startIdx+1 {
			env.Request.Messages = append(env.Request.Messages[:startIdx], env.Request.Messages[startIdx+1:]...)
			slog.Debug("pruned old message to fit context window", "request_id", env.RequestID, "current_count", count)
		} else {
			break
		}
	}
}

// messagesToText 将消息列表展平为纯文本以便进行令牌（Token）计数。
func messagesToText(msgs []models.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(m.GetText())
		sb.WriteString(" ")
	}
	return sb.String()
}
