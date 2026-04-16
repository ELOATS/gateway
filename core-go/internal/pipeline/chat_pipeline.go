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
	"github.com/ai-gateway/core/internal/cache"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/dependencies"
	"github.com/ai-gateway/core/internal/middleware"
	"github.com/ai-gateway/core/internal/nitro"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/quota"
	"github.com/ai-gateway/core/internal/router"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
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

type RequestEnvelope struct {
	RequestID string
	APIKey    string
	KeyLabel  string
	SessionID string
	Model     string
	Prompt    string
	Start     time.Time
	Request   *models.ChatCompletionRequest
}

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

type ExecutionPlan struct {
	RouteContext *router.RouteContext
	Node         *router.ModelNode
	Cached       *models.ChatCompletionResponse
}

type ExecutionResult struct {
	Response      *models.ChatCompletionResponse
	Stream        <-chan *models.ChatCompletionStreamResponse
	StreamErrors  <-chan error
	Node          *router.ModelNode
	FromCache     bool
	Degraded      bool
	DegradeReason string
}

type ChatPipeline struct {
	policyEngine *PolicyEngine
	dependencies *dependencies.Facade
	router       *router.SmartRouter
	config       *config.Config
	rdb          *redis.Client
	contextStore *cache.ContextStore
	now          func() time.Time
}

func NewChatPipeline(ic pb.AiLogicClient, nc nitro.NitroClient, sr *router.SmartRouter, rdb *redis.Client, cfg *config.Config) *ChatPipeline {
	RegisterPolicies()

	facade := dependencies.NewFacade(ic, nc, cfg)
	deps := &DependencyContainer{
		Dependencies: facade,
		RedisClient:  rdb,
		Config:       cfg,
	}

	policyPath := resolvePolicyPath(cfg)
	engine, err := NewPolicyEngine(policyPath, deps)
	if err != nil {
		slog.Error("failed to load policy engine, requests may be blocked", "path", policyPath, "error", err)
	}

	return &ChatPipeline{
		policyEngine: engine,
		dependencies: facade,
		router:       sr,
		config:       cfg,
		rdb:          rdb,
		contextStore: cache.NewContextStore(rdb, 24*time.Hour),
		now:          time.Now,
	}
}

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
		SessionID: c.GetHeader("X-Session-ID"),
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

func (p *ChatPipeline) EvaluatePolicies(c *gin.Context, ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	if p.policyEngine == nil {
		return &PolicyDecision{
			StatusCode: http.StatusServiceUnavailable,
			ErrorCode:  "engine_unavailable",
			Message:    "Policy engine is not initialized",
			Reason:     "initialization failure",
		}
	}

	decision := p.policyEngine.Evaluate(ctx, env)
	if !decision.Allow {
		return decision
	}

	replaceLastMessage(env.Request, decision.SanitizedPrompt)
	return decision
}

func (p *ChatPipeline) BuildPlan(ctx context.Context, c *gin.Context, env *RequestEnvelope) (*ExecutionPlan, *PolicyDecision) {
	p.asyncCountTokens(env.RequestID, env.Prompt, env.Model)

	if env.SessionID != "" && p.contextStore != nil {
		history, err := p.contextStore.Retrieve(ctx, env.SessionID)
		if err == nil && len(history) > 0 {
			if len(history) > 10 {
				history = history[len(history)-10:]
			}
			env.Request.Messages = append(history, env.Request.Messages...)
			slog.Debug("session context injected", "session_id", env.SessionID, "history_len", len(history))
		}
	}

	if !env.Request.Stream {
		if cached, degraded, degradeReason, hardFailure := p.checkCache(ctx, env); cached != nil {
			return &ExecutionPlan{Cached: cached}, &PolicyDecision{Allow: true, Degraded: degraded, DegradeReason: degradeReason}
		} else if hardFailure {
			return nil, &PolicyDecision{
				StatusCode: http.StatusServiceUnavailable,
				ErrorCode:  "python_cache_unavailable",
				Message:    "semantic cache unavailable",
				Reason:     degradeReason,
			}
		} else if degradeReason != "" || degraded {
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

	combinedDegraded := degraded
	combinedReason := degradeReason

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, &PolicyDecision{StatusCode: http.StatusRequestTimeout, ErrorCode: "request_cancelled", Message: "request cancelled while retrying provider", Reason: "execution context cancelled"}
			}
		}

		routeCtx := *plan.RouteContext
		routeCtx.ExcludeNodes = excluded
		node, err := p.router.Route(&routeCtx)
		if err != nil {
			return nil, &PolicyDecision{StatusCode: http.StatusServiceUnavailable, ErrorCode: "routing_error", Message: err.Error(), Reason: "no execution target available"}
		}

		callStart := p.now()
		resp, err := node.Adapter.ChatCompletion(ctx, env.Request)
		callDuration := p.now().Sub(callStart)
		if err != nil {
			p.router.Tracker.RecordFailure(node.Name)
			recordProviderError(node.Name, err)
			if attempt < maxRetries {
				excluded = append(excluded, node.Name)
				continue
			}
			return nil, &PolicyDecision{StatusCode: http.StatusBadGateway, ErrorCode: "provider_error", Message: err.Error(), Reason: "all configured providers failed"}
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
			go quota.UpdateUsage(context.Background(), p.rdb, env.APIKey, int64(response.Usage.TotalTokens))
		}

		return &ExecutionResult{Response: response, Node: node, FromCache: false, Degraded: combinedDegraded, DegradeReason: combinedReason}, nil
	}

	return nil, &PolicyDecision{StatusCode: http.StatusBadGateway, ErrorCode: "provider_error", Message: "provider execution failed", Reason: "unreachable execution state"}
}

func (p *ChatPipeline) ExecuteStream(ctx context.Context, env *RequestEnvelope, plan *ExecutionPlan, degraded bool, degradeReason string) (*ExecutionResult, *PolicyDecision) {
	if plan.Cached != nil {
		return nil, &PolicyDecision{StatusCode: http.StatusBadRequest, ErrorCode: "stream_cache_misshaped", Message: "cached responses are not available for SSE replay", Reason: "stream requests require provider streaming", Degraded: degraded}
	}
	stream, errCh := plan.Node.Adapter.ChatCompletionStream(ctx, env.Request)
	return &ExecutionResult{Stream: stream, StreamErrors: errCh, Node: plan.Node, Degraded: degraded, DegradeReason: degradeReason}, nil
}

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
			return &PolicyDecision{StatusCode: http.StatusForbidden, ErrorCode: "moderation_triggered", Message: "[content blocked: stream safety policy triggered]", Reason: badWord}
		}
	}
	return nil
}

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

	p.logAudit(env, nil, EventRejected, strconv.Itoa(decision.StatusCode), decision.Reason, "", decision.Degraded, decision.DegradeReason, 0)
	c.JSON(decision.StatusCode, payload)
}

func (p *ChatPipeline) RecordExecutionStarted(env *RequestEnvelope, nodeName string, degraded bool, degradeReason string) {
	p.logAudit(env, nodeNamePtr(nodeName), EventStarted, "accepted", "", "", degraded, degradeReason, 0)
}

func (p *ChatPipeline) RecordExecutionCompleted(env *RequestEnvelope, nodeName string, responseText string, tokens int, degraded bool, degradeReason string, status string) {
	p.logAudit(env, nodeNamePtr(nodeName), EventCompleted, status, "", responseText, degraded, degradeReason, tokens)

	if env.SessionID != "" && p.contextStore != nil && status == "200" && responseText != "" {
		go func() {
			currentRound := []models.Message{{Role: "user", Content: env.Prompt}, {Role: "assistant", Content: responseText}}
			_ = p.contextStore.Append(context.Background(), env.SessionID, currentRound)
		}()
	}
}

func (p *ChatPipeline) RecordStreamBlocked(env *RequestEnvelope, nodeName string, responseText string, reason string, degraded bool, degradeReason string, tokens int) {
	p.logAudit(env, nodeNamePtr(nodeName), EventStreamStop, "blocked", reason, responseText, degraded, degradeReason, tokens)
}

func (p *ChatPipeline) RecordDegraded(env *RequestEnvelope, nodeName string, reason string) {
	observability.RecordDegradedEvent("pipeline", reason)
	p.logAudit(env, nodeNamePtr(nodeName), EventDegraded, "accepted", reason, "", true, reason, 0)
}

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
			p.logAudit(env, &nodeName, EventStreamStop, "violation", "unsafe content detected in stream", fullText, true, "async moderation failed", 0)
		}
	}()
}

func (p *ChatPipeline) guardOutputResponse(ctx context.Context, env *RequestEnvelope, node *router.ModelNode, resp *models.ChatCompletionResponse) (*models.ChatCompletionResponse, *PolicyDecision) {
	if resp == nil || len(resp.Choices) == 0 || p.dependencies == nil {
		return resp, nil
	}

	outcome := p.dependencies.CheckOutput(ctx, resp.Choices[0].Message.GetText())
	if !outcome.Allowed {
		return resp, &PolicyDecision{StatusCode: outcome.StatusCode, ErrorCode: outcome.ErrorCode, Message: outcome.Message, Reason: outcome.Reason}
	}

	resp.Choices[0].Message.Content = outcome.SanitizedText
	if outcome.Degraded {
		return resp, &PolicyDecision{Allow: true, Degraded: true, DegradeReason: outcome.DegradeReason}
	}
	return resp, nil
}

func (p *ChatPipeline) checkCache(ctx context.Context, env *RequestEnvelope) (*models.ChatCompletionResponse, bool, string, bool) {
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

	observability.CacheHitsTotal.WithLabelValues("hit", env.Model).Inc()
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

func (p *ChatPipeline) logAudit(env *RequestEnvelope, nodeName *string, event string, status string, reason string, response string, degraded bool, degradeReason string, tokens int) {
	if observability.GlobalAuditLogger == nil {
		return
	}
	record := &observability.AuditRecord{Timestamp: p.now(), Event: event, Status: status, Reason: joinReasons(reason, degradeReason), Degraded: degraded, Response: response, Tokens: tokens}
	if env != nil {
		record.RequestID = env.RequestID
		record.Model = env.Model
		
		maskedKey := env.APIKey
		if len(maskedKey) > 10 {
			maskedKey = maskedKey[:6] + "***" + maskedKey[len(maskedKey)-4:]
		}
		record.APIKey = maskedKey
		
		truncatedPrompt := env.Prompt
		if len(truncatedPrompt) > 1000 {
			truncatedPrompt = truncatedPrompt[:1000] + "...(truncated)"
		}
		record.Prompt = truncatedPrompt
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
