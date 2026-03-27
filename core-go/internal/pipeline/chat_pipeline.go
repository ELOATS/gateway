package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/middleware"
	"github.com/ai-gateway/core/internal/nitro"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/router"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

const (
	// 审计事件名统一由 pipeline 定义，避免不同执行路径写出不一致的事件语义。
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
	"大语言模型不能",
}

// RequestEnvelope 是统一热路径中的“请求真相源”。
// 进入策略阶段后，后续逻辑只消费这个结构，不再反复从 Gin 或原始 JSON 中取值。
type RequestEnvelope struct {
	RequestID string
	APIKey    string
	KeyLabel  string
	Model     string
	Prompt    string
	Start     time.Time
	Request   *models.ChatCompletionRequest
}

// PolicyDecision 表示同步策略阶段的唯一输出。
// 任意拒绝、放行、降级都需要先落到这个结构，再决定是否进入执行阶段。
type PolicyDecision struct {
	Allow           bool
	StatusCode      int
	ErrorCode       string
	Message         string
	Reason          string
	SanitizedPrompt string
	RetryAfter      string
	Degraded        bool
	DegradeReason   string
}

// ExecutionPlan 表示已经完成策略决策后的执行计划。
// 它要么指向缓存命中结果，要么指向一个明确的上游路由目标。
type ExecutionPlan struct {
	RouteContext *router.RouteContext
	Node         *router.ModelNode
	Cached       *models.ChatCompletionResponse
}

// ExecutionResult 是执行阶段的统一返回值。
// 同步响应和流式响应都被包装成这一层，便于后续审计和输出护栏共用一套语义。
type ExecutionResult struct {
	Response      *models.ChatCompletionResponse
	Stream        <-chan *models.ChatCompletionStreamResponse
	StreamErrors  <-chan error
	Node          *router.ModelNode
	FromCache     bool
	Degraded      bool
	DegradeReason string
}

type localLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// ChatPipeline 将“规范化 -> 策略 -> 规划 -> 执行 -> 审计”收敛到一个协调器中。
// Gin handler 只负责驱动它，不再自己承载零散的业务判断。
type ChatPipeline struct {
	intelligenceClient pb.AiLogicClient
	nitroClient        nitro.NitroClient
	router             *router.SmartRouter
	config             *config.Config
	rdb                *redis.Client

	mu            sync.Mutex
	localLimiters map[string]*localLimiter
	now           func() time.Time
}

// NewChatPipeline 初始化统一请求管线，并启动本地限流器的过期清理协程。
func NewChatPipeline(ic pb.AiLogicClient, nc nitro.NitroClient, sr *router.SmartRouter, rdb *redis.Client, cfg *config.Config) *ChatPipeline {
	p := &ChatPipeline{
		intelligenceClient: ic,
		nitroClient:        nc,
		router:             sr,
		config:             cfg,
		rdb:                rdb,
		localLimiters:      make(map[string]*localLimiter),
		now:                time.Now,
	}
	go p.cleanupLocalLimiters()
	return p
}

// Normalize 只做请求解析和最小字段提取，不做任何策略判断。
func (p *ChatPipeline) Normalize(c *gin.Context, start time.Time) (*RequestEnvelope, *PolicyDecision) {
	var req models.ChatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		return nil, &PolicyDecision{
			StatusCode: http.StatusBadRequest,
			ErrorCode:  "invalid_payload",
			Message:    err.Error(),
			Reason:     "request payload could not be parsed",
		}
	}

	env := &RequestEnvelope{
		RequestID: c.GetString(middleware.RequestIDKey),
		APIKey:    c.GetString("api_key"),
		KeyLabel:  c.GetString("key_label"),
		Model:     req.Model,
		Prompt:    extractPrompt(&req),
		Start:     start,
		Request:   &req,
	}
	if env.KeyLabel == "" {
		env.KeyLabel = "anonymous"
	}
	return env, nil
}

// EvaluatePolicies 按固定顺序执行工具授权、限流、配额和输入护栏。
// 这一步是进入上游执行前唯一允许做“是否放行”判断的地方。
func (p *ChatPipeline) EvaluatePolicies(c *gin.Context, ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	if decision := p.authorizeTools(env); decision != nil {
		return decision
	}
	if decision := p.admitRateLimit(ctx, env); decision != nil {
		return decision
	}
	if decision := p.admitQuota(ctx, env); decision != nil {
		return decision
	}

	sanitizedPrompt, decision := p.guardInput(ctx, env)
	if decision != nil && !decision.Allow {
		return decision
	}

	env.Prompt = sanitizedPrompt
	replaceLastMessage(env.Request, sanitizedPrompt)
	if decision == nil {
		decision = &PolicyDecision{}
	}
	decision.Allow = true
	decision.SanitizedPrompt = sanitizedPrompt
	return decision
}

// BuildPlan 在策略阶段通过后生成执行计划。
// 非流式请求会先尝试缓存命中；否则根据路由器得到一个明确的执行目标。
func (p *ChatPipeline) BuildPlan(ctx context.Context, c *gin.Context, env *RequestEnvelope) (*ExecutionPlan, *PolicyDecision) {
	p.asyncCountTokens(env.RequestID, env.Prompt, env.Model)

	if !env.Request.Stream {
		if cached, degraded, degradeReason, hardFailure := p.checkCache(ctx, env); cached != nil {
			return &ExecutionPlan{Cached: cached}, &PolicyDecision{
				Allow:         true,
				Degraded:      degraded,
				DegradeReason: degradeReason,
			}
		} else if hardFailure {
			return nil, &PolicyDecision{
				StatusCode: http.StatusServiceUnavailable,
				ErrorCode:  "python_cache_unavailable",
				Message:    "semantic cache unavailable",
				Reason:     degradeReason,
			}
		} else if degradeReason != "" || degraded {
			// 将非阻断型降级显式写入上下文，便于最终审计统一落盘。
			c.Set("pipeline_cache_degraded", degraded)
			c.Set("pipeline_cache_degrade_reason", degradeReason)
		}
	}

	if p.router == nil {
		return nil, &PolicyDecision{
			StatusCode: http.StatusServiceUnavailable,
			ErrorCode:  "routing_error",
			Message:    "router is not configured",
			Reason:     "execution planner unavailable",
		}
	}

	routeCtx := &router.RouteContext{
		RequestID:    env.RequestID,
		Model:        env.Request.Model,
		PromptTokens: len(env.Prompt) / maxInt(p.config.TokenEstimationFactor, 1),
		UserTier:     env.KeyLabel,
		Headers:      map[string]string{"X-Route-Strategy": c.GetHeader("X-Route-Strategy")},
	}

	node, err := p.router.Route(routeCtx)
	if err != nil {
		return nil, &PolicyDecision{
			StatusCode: http.StatusServiceUnavailable,
			ErrorCode:  "routing_error",
			Message:    err.Error(),
			Reason:     "no execution target available",
		}
	}

	return &ExecutionPlan{RouteContext: routeCtx, Node: node}, nil
}

// ExecuteSync 执行非流式请求，并在 provider 调用后统一补上输出护栏和降级语义。
func (p *ChatPipeline) ExecuteSync(ctx context.Context, env *RequestEnvelope, plan *ExecutionPlan, degraded bool, degradeReason string) (*ExecutionResult, *PolicyDecision) {
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

	var combinedDegraded = degraded
	combinedReason := degradeReason

	// 重试只发生在已经通过策略校验之后；失败节点会被排除，避免同一上游被重复命中。
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, &PolicyDecision{
					StatusCode: http.StatusRequestTimeout,
					ErrorCode:  "request_cancelled",
					Message:    "request cancelled while retrying provider",
					Reason:     "execution context cancelled",
				}
			}
		}

		routeCtx := *plan.RouteContext
		routeCtx.ExcludeNodes = excluded
		node, err := p.router.Route(&routeCtx)
		if err != nil {
			return nil, &PolicyDecision{
				StatusCode: http.StatusServiceUnavailable,
				ErrorCode:  "routing_error",
				Message:    err.Error(),
				Reason:     "no execution target available",
			}
		}

		callStart := p.now()
		resp, err := node.Adapter.ChatCompletion(env.Request)
		callDuration := p.now().Sub(callStart)
		if err != nil {
			p.router.Tracker.RecordFailure(node.Name)
			recordProviderError(node.Name, err)
			if attempt < maxRetries {
				excluded = append(excluded, node.Name)
				continue
			}
			return nil, &PolicyDecision{
				StatusCode: http.StatusBadGateway,
				ErrorCode:  "provider_error",
				Message:    err.Error(),
				Reason:     "all configured providers failed",
			}
		}

		p.router.Tracker.RecordSuccess(node.Name, callDuration)
		response, outputDecision := p.guardOutputResponse(ctx, env, node, resp)
		if outputDecision != nil && !outputDecision.Allow {
			return nil, outputDecision
		}

		if outputDecision != nil && outputDecision.Degraded {
			combinedDegraded = true
			combinedReason = joinReasons(combinedReason, outputDecision.DegradeReason)
		}

		if env.APIKey != "" && response.Usage.TotalTokens > 0 && p.rdb != nil {
			go middleware.UpdateQuotaUsage(context.Background(), p.rdb, env.APIKey, int64(response.Usage.TotalTokens))
		}

		return &ExecutionResult{
			Response:      response,
			Node:          node,
			FromCache:     false,
			Degraded:      combinedDegraded,
			DegradeReason: combinedReason,
		}, nil
	}

	return nil, &PolicyDecision{
		StatusCode: http.StatusBadGateway,
		ErrorCode:  "provider_error",
		Message:    "provider execution failed",
		Reason:     "unreachable execution state",
	}
}

// ExecuteStream 只负责建立流式上游通道。
// 真正的 chunk 审查、SSE 输出和审计仍由 handler 驱动，但语义来源保持一致。
func (p *ChatPipeline) ExecuteStream(env *RequestEnvelope, plan *ExecutionPlan, degraded bool, degradeReason string) (*ExecutionResult, *PolicyDecision) {
	if plan.Cached != nil {
		return nil, &PolicyDecision{
			StatusCode: http.StatusBadRequest,
			ErrorCode:  "stream_cache_misshaped",
			Message:    "cached responses are not available for SSE replay",
			Reason:     "stream requests require provider streaming",
			Degraded:   degraded,
		}
	}
	stream, errCh := plan.Node.Adapter.ChatCompletionStream(env.Request)
	return &ExecutionResult{
		Stream:        stream,
		StreamErrors:  errCh,
		Node:          plan.Node,
		Degraded:      degraded,
		DegradeReason: degradeReason,
	}, nil
}

// GuardStreamChunk 对流式输出做滑动窗口审查，避免敏感短语跨 chunk 漏检。
func (p *ChatPipeline) GuardStreamChunk(window *strings.Builder, chunk string) *PolicyDecision {
	window.WriteString(chunk)
	text := window.String()
	runes := []rune(text)
	if len(runes) > 100 {
		text = string(runes[len(runes)-100:])
		window.Reset()
		window.WriteString(text)
	}

	lower := strings.ToLower(text)
	for _, badWord := range streamModerationBlacklist {
		if strings.Contains(lower, badWord) {
			return &PolicyDecision{
				StatusCode: http.StatusForbidden,
				ErrorCode:  "moderation_triggered",
				Message:    "[内容检测中止：触发流式安全防护]",
				Reason:     badWord,
			}
		}
	}
	return nil
}

// RespondDecision 将策略阶段产生的拒绝结果统一映射为 HTTP 响应和审计事件。
func (p *ChatPipeline) RespondDecision(c *gin.Context, env *RequestEnvelope, decision *PolicyDecision) {
	if decision == nil {
		return
	}
	if decision.Allow && decision.StatusCode == 0 {
		return
	}

	if decision.RetryAfter != "" {
		c.Header("Retry-After", decision.RetryAfter)
	}

	model := ""
	if env != nil {
		model = env.Model
	}
	if model == "" {
		model = "unknown"
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

	p.logAudit(env, nil, EventRejected, strconv.Itoa(decision.StatusCode), decision.Reason, "", decision.Degraded, decision.DegradeReason, 0)
	c.JSON(decision.StatusCode, payload)
}

// RecordExecutionStarted/Completed/StreamBlocked/Degraded 统一封装审计事件，避免不同路径手写字段。
func (p *ChatPipeline) RecordExecutionStarted(env *RequestEnvelope, nodeName string, degraded bool, degradeReason string) {
	p.logAudit(env, nodeNamePtr(nodeName), EventStarted, "accepted", "", "", degraded, degradeReason, 0)
}

func (p *ChatPipeline) RecordExecutionCompleted(env *RequestEnvelope, nodeName string, responseText string, tokens int, degraded bool, degradeReason string, status string) {
	p.logAudit(env, nodeNamePtr(nodeName), EventCompleted, status, "", responseText, degraded, degradeReason, tokens)
}

func (p *ChatPipeline) RecordStreamBlocked(env *RequestEnvelope, nodeName string, responseText string, reason string, degraded bool, degradeReason string, tokens int) {
	p.logAudit(env, nodeNamePtr(nodeName), EventStreamStop, "blocked", reason, responseText, degraded, degradeReason, tokens)
}

func (p *ChatPipeline) RecordDegraded(env *RequestEnvelope, nodeName string, reason string) {
	observability.RecordDegradedEvent("pipeline", reason)
	p.logAudit(env, nodeNamePtr(nodeName), EventDegraded, "accepted", reason, "", true, reason, 0)
}

func (p *ChatPipeline) authorizeTools(env *RequestEnvelope) *PolicyDecision {
	if len(env.Request.Tools) == 0 && env.Request.ToolChoice == nil {
		return nil
	}
	if env.KeyLabel == "admin" || env.KeyLabel == "premium" {
		return nil
	}
	return &PolicyDecision{
		StatusCode: http.StatusForbidden,
		ErrorCode:  "tool_call_forbidden",
		Message:    "The active API Key tier does not have permission to utilize Agentic tools or function calling.",
		Reason:     "tool usage requires admin or premium tier",
	}
}

func (p *ChatPipeline) admitRateLimit(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	if p.config.RateLimitQPS <= 0 || p.config.RateLimitBurst <= 0 {
		return nil
	}

	label := env.KeyLabel
	if label == "" {
		label = "anonymous"
	}

	if p.rdb != nil {
		allowed, err := checkRedisRateLimit(ctx, p.rdb, label, p.config.RateLimitQPS, p.config.RateLimitBurst)
		if err == nil {
			if !allowed {
				observability.RateLimitedTotal.WithLabelValues(label).Inc()
				return &PolicyDecision{
					StatusCode: http.StatusTooManyRequests,
					ErrorCode:  "rate_limit_exceeded",
					Message:    "请求过于频繁，请稍后再试。",
					Reason:     "distributed rate limit exceeded",
					RetryAfter: retryAfterSeconds(p.config.RateLimitQPS),
				}
			}
			return nil
		}
		slog.Warn("distributed rate limit degraded to local limiter", "request_id", env.RequestID, "error", err)
	}

	p.mu.Lock()
	le, ok := p.localLimiters[label]
	if !ok {
		le = &localLimiter{limiter: rate.NewLimiter(rate.Limit(p.config.RateLimitQPS), p.config.RateLimitBurst)}
		p.localLimiters[label] = le
	}
	le.lastSeen = p.now()
	allow := le.limiter.Allow()
	p.mu.Unlock()

	if !allow {
		observability.RateLimitedTotal.WithLabelValues(label).Inc()
		return &PolicyDecision{
			StatusCode: http.StatusTooManyRequests,
			ErrorCode:  "rate_limit_exceeded",
			Message:    "请求过于频繁，请稍后再试。",
			Reason:     "local rate limit exceeded",
			RetryAfter: retryAfterSeconds(p.config.RateLimitQPS),
		}
	}
	return nil
}

func (p *ChatPipeline) admitQuota(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	if env.APIKey == "" || p.rdb == nil {
		return nil
	}

	var limit int64
	for _, entry := range p.config.APIKeys {
		if entry.Key == env.APIKey {
			limit = entry.DailyQuota
			break
		}
	}
	if limit <= 0 {
		return nil
	}

	redisKey := fmt.Sprintf("quota:usage:%s", env.APIKey)
	usageStr, err := p.rdb.Get(ctx, redisKey).Result()
	if errors.Is(err, redis.Nil) {
		usageStr = "0"
	} else if err != nil {
		slog.Warn("quota check degraded", "request_id", env.RequestID, "error", err)
		return nil
	}

	usage, _ := strconv.ParseInt(usageStr, 10, 64)
	if usage < limit {
		return nil
	}

	ttl, _ := p.rdb.TTL(ctx, redisKey).Result()
	if ttl < 0 {
		ttl = 24 * time.Hour
	}

	return &PolicyDecision{
		StatusCode: http.StatusTooManyRequests,
		ErrorCode:  "quota_exceeded",
		Message:    fmt.Sprintf("您的配额已耗尽 (%d/%d Tokens)。将在 %d 小时 %d 分钟后重置。", usage, limit, int(ttl.Hours()), int(ttl.Minutes())%60),
		Reason:     "daily token quota exhausted",
	}
}

func (p *ChatPipeline) guardInput(ctx context.Context, env *RequestEnvelope) (string, *PolicyDecision) {
	prompt := env.Prompt

	// Nitro 属于同步安全关键路径，默认按 fail-closed 处理。
	if p.nitroClient != nil {
		nitroCtx, cancelNitro := context.WithTimeout(ctx, p.config.GuardrailNitroTimeout)
		defer cancelNitro()

		sanitized, err := p.nitroClient.CheckInput(nitroCtx, prompt)
		if err != nil {
			if strings.EqualFold(p.config.NitroFailureMode, "fail_open_with_audit") {
				return prompt, &PolicyDecision{
					Allow:           true,
					Degraded:        true,
					DegradeReason:   "nitro input guardrail unavailable",
					SanitizedPrompt: prompt,
				}
			}
			return prompt, &PolicyDecision{
				StatusCode: http.StatusServiceUnavailable,
				ErrorCode:  "guardrail_unavailable",
				Message:    "input guardrail unavailable",
				Reason:     "nitro safety check failed",
			}
		}
		prompt = sanitized
	}

	if p.intelligenceClient == nil {
		return prompt, &PolicyDecision{Allow: true}
	}

	pyCtx, cancelPy := context.WithTimeout(ctx, p.config.GuardrailIntellTimeout)
	defer cancelPy()

	// Python 输入护栏属于可配置增强能力，可按 failure mode 决定 fail-open 还是 fail-closed。
	pyResp, err := p.intelligenceClient.CheckInput(pyCtx, &pb.InputRequest{Prompt: prompt})
	if err != nil {
		if strings.EqualFold(p.config.PythonInputFailureMode, "fail_closed") {
			return prompt, &PolicyDecision{
				StatusCode: http.StatusServiceUnavailable,
				ErrorCode:  "python_guardrail_unavailable",
				Message:    "python input guardrail unavailable",
				Reason:     "python input guardrail failure",
			}
		}
		slog.Warn("python input guardrail degraded", "request_id", env.RequestID, "error", err)
		return prompt, &PolicyDecision{
			Allow:           true,
			Degraded:        true,
			DegradeReason:   "python input guardrail unavailable",
			SanitizedPrompt: prompt,
		}
	}

	if !pyResp.Safe {
		return prompt, &PolicyDecision{
			StatusCode: http.StatusForbidden,
			ErrorCode:  "security_block",
			Message:    pyResp.Reason,
			Reason:     pyResp.Reason,
		}
	}
	return pyResp.SanitizedPrompt, &PolicyDecision{
		Allow:           true,
		SanitizedPrompt: pyResp.SanitizedPrompt,
	}
}

func (p *ChatPipeline) guardOutputResponse(ctx context.Context, env *RequestEnvelope, node *router.ModelNode, resp *models.ChatCompletionResponse) (*models.ChatCompletionResponse, *PolicyDecision) {
	if resp == nil || len(resp.Choices) == 0 || p.intelligenceClient == nil {
		return resp, nil
	}

	text := resp.Choices[0].Message.GetText()
	pyCtx, cancelPy := context.WithTimeout(ctx, p.config.GuardrailIntellTimeout)
	defer cancelPy()

	// 输出护栏放在 provider 返回之后统一执行，保证同步与缓存路径语义一致。
	pyResp, err := p.intelligenceClient.CheckOutput(pyCtx, &pb.OutputRequest{ResponseText: text})
	if err != nil {
		if strings.EqualFold(p.config.PythonOutputFailureMode, "fail_closed") {
			return resp, &PolicyDecision{
				StatusCode: http.StatusServiceUnavailable,
				ErrorCode:  "python_output_guardrail_unavailable",
				Message:    "python output guardrail unavailable",
				Reason:     "python output guardrail failure",
			}
		}
		slog.Warn("python output guardrail degraded", "request_id", env.RequestID, "error", err)
		return resp, &PolicyDecision{
			Allow:         true,
			Degraded:      true,
			DegradeReason: "python output guardrail unavailable",
		}
	}

	resp.Choices[0].Message.Content = pyResp.SanitizedText
	if !pyResp.Safe {
		return resp, &PolicyDecision{
			Allow:           true,
			Degraded:        true,
			DegradeReason:   "output sanitized by guardrail",
			SanitizedPrompt: pyResp.SanitizedText,
		}
	}
	return resp, nil
}

func (p *ChatPipeline) checkCache(ctx context.Context, env *RequestEnvelope) (*models.ChatCompletionResponse, bool, string, bool) {
	if p.intelligenceClient == nil {
		return nil, false, "", false
	}

	cacheCtx, cancel := context.WithTimeout(ctx, p.config.CacheTimeout)
	defer cancel()

	// 缓存只影响“是否继续执行上游”，不允许改变基础安全语义。
	cacheResp, err := p.intelligenceClient.GetCache(cacheCtx, &pb.CacheRequest{
		Prompt: env.Prompt,
		Model:  env.Model,
	})
	if err != nil {
		if strings.EqualFold(p.config.PythonCacheFailureMode, "fail_closed") {
			return nil, false, "semantic cache unavailable", true
		}
		slog.Warn("semantic cache degraded", "request_id", env.RequestID, "error", err)
		observability.CacheHitsTotal.WithLabelValues("miss", env.Model).Inc()
		return nil, true, "semantic cache unavailable", false
	}
	if !cacheResp.Hit {
		observability.CacheHitsTotal.WithLabelValues("miss", env.Model).Inc()
		return nil, false, "", false
	}

	observability.CacheHitsTotal.WithLabelValues("hit", env.Model).Inc()
	return &models.ChatCompletionResponse{
		ID:    fmt.Sprintf("cache-%s", env.RequestID),
		Model: env.Model,
		Choices: []models.Choice{{
			Index: 0,
			Message: models.Message{
				Role:    "assistant",
				Content: cacheResp.Response,
			},
			FinishReason: "stop",
		}},
	}, false, "", false
}

func (p *ChatPipeline) asyncCountTokens(rid, text, model string) {
	if p.nitroClient == nil {
		return
	}
	// Token 统计属于异步补偿路径，失败不影响主请求返回。
	go func() {
		tCtx, cancel := context.WithTimeout(context.Background(), p.config.TokenCountTimeout)
		defer cancel()
		tCtx = observability.NewOutContext(tCtx, rid)
		count, err := p.nitroClient.CountTokens(tCtx, model, text)
		if err == nil {
			observability.TokenUsage.WithLabelValues(model).Add(float64(count))
		}
	}()
}

func (p *ChatPipeline) cleanupLocalLimiters() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		// Redis 不可用时才会走本地限流器，因此定期回收长期未访问的租户状态。
		p.mu.Lock()
		now := p.now()
		for key, limiter := range p.localLimiters {
			if now.Sub(limiter.lastSeen) > time.Hour {
				delete(p.localLimiters, key)
			}
		}
		p.mu.Unlock()
	}
}

func (p *ChatPipeline) logAudit(env *RequestEnvelope, nodeName *string, event string, status string, reason string, response string, degraded bool, degradeReason string, tokens int) {
	if observability.GlobalAuditLogger == nil {
		return
	}
	record := &observability.AuditRecord{
		Timestamp: p.now(),
		Event:     event,
		Status:    status,
		Reason:    joinReasons(reason, degradeReason),
		Degraded:  degraded,
		Response:  response,
		Tokens:    tokens,
	}
	if env != nil {
		record.RequestID = env.RequestID
		record.APIKey = env.APIKey
		record.Model = env.Model
		record.Prompt = env.Prompt
	}
	if nodeName != nil {
		record.Node = *nodeName
	}
	observability.GlobalAuditLogger.Log(record)
}

func extractPrompt(req *models.ChatCompletionRequest) string {
	if req == nil || len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[len(req.Messages)-1].GetText()
}

func replaceLastMessage(req *models.ChatCompletionRequest, prompt string) {
	if req == nil || len(req.Messages) == 0 {
		return
	}
	req.Messages[len(req.Messages)-1].Content = prompt
}

func checkRedisRateLimit(ctx context.Context, rdb *redis.Client, label string, qps float64, _ int) (bool, error) {
	key := "rl:" + label
	now := time.Now().UnixNano() / 1e6
	window := int64(1000)
	limit := int64(qps)
	if limit <= 0 {
		limit = 1
	}

	script := `
		local key = KEYS[1]
		local now = tonumber(ARGV[1])
		local window = tonumber(ARGV[2])
		local limit = tonumber(ARGV[3])

		redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
		local count = redis.call('ZCARD', key)
		if count >= limit then
			return 0
		else
			redis.call('ZADD', key, now, now)
			redis.call('PEXPIRE', key, window)
			return 1
		end
	`
	res, err := rdb.Eval(ctx, script, []string{key}, now, window, limit).Int()
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

func MarshalSSEData(resp *models.ChatCompletionStreamResponse) []byte {
	data, _ := json.Marshal(resp)
	return data
}
