package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/nitro"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// Policy 定义一个策略评估步骤的统一接口。
// 每个策略只负责判断，不持有基础设施依赖。
type Policy interface {
	Name() string
	Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision
	Close() // 用于在策略引擎重新加载时清理背景资源
}

// RegisterPolicies 将项目中所有可用的策略实现注册到引擎中。
func RegisterPolicies() {
	RegisterPolicy("tool_auth", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		return &ToolAuthPolicy{}, nil
	})

	RegisterPolicy("rate_limit", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		return NewRateLimitPolicy(deps.Config, deps.RedisClient), nil
	})

	RegisterPolicy("quota", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		return &QuotaPolicy{config: deps.Config, rdb: deps.RedisClient}, nil
	})

	RegisterPolicy("input_guard", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		return &InputGuardPolicy{
			nitroClient:        deps.NitroClient,
			intelligenceClient: deps.IntelligenceClient,
			config:             deps.Config,
		}, nil
	})
}

// ToolAuthPolicy 工具级权限验证策略
type ToolAuthPolicy struct{}

func (p *ToolAuthPolicy) Name() string { return "tool_auth" }
func (p *ToolAuthPolicy) Close()       {}
func (p *ToolAuthPolicy) Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
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

type localLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimitPolicy 速率限制策略
type RateLimitPolicy struct {
	config        *config.Config
	rdb           *redis.Client
	mu            sync.Mutex
	localLimiters map[string]*localLimiter
	now           func() time.Time
	cancel        context.CancelFunc
}

func NewRateLimitPolicy(cfg *config.Config, rdb *redis.Client) *RateLimitPolicy {
	ctx, cancel := context.WithCancel(context.Background())
	rp := &RateLimitPolicy{
		config:        cfg,
		rdb:           rdb,
		localLimiters: make(map[string]*localLimiter),
		now:           time.Now,
		cancel:        cancel,
	}
	go rp.cleanupLocalLimiters(ctx)
	return rp
}

func (rp *RateLimitPolicy) Close() {
	if rp.cancel != nil {
		rp.cancel()
	}
}

func (rp *RateLimitPolicy) cleanupLocalLimiters(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rp.mu.Lock()
			now := rp.now()
			for key, limiter := range rp.localLimiters {
				if now.Sub(limiter.lastSeen) > time.Hour {
					delete(rp.localLimiters, key)
				}
			}
			rp.mu.Unlock()
		}
	}
}

func (rp *RateLimitPolicy) Name() string { return "rate_limit" }
func (rp *RateLimitPolicy) Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	if rp.config.RateLimitQPS <= 0 || rp.config.RateLimitBurst <= 0 {
		return nil
	}

	label := env.KeyLabel
	if label == "" {
		label = "anonymous"
	}

	if rp.rdb != nil {
		allowed, err := checkRedisRateLimit(ctx, rp.rdb, label, rp.config.RateLimitQPS, rp.config.RateLimitBurst)
		if err == nil {
			if !allowed {
				observability.RateLimitedTotal.WithLabelValues(label).Inc()
				return &PolicyDecision{
					StatusCode: http.StatusTooManyRequests,
					ErrorCode:  "rate_limit_exceeded",
					Message:    "请求过于频繁，请稍后再试。",
					Reason:     "distributed rate limit exceeded",
					RetryAfter: retryAfterSeconds(rp.config.RateLimitQPS),
				}
			}
			return nil
		}
		slog.Warn("distributed rate limit degraded to local limiter", "request_id", env.RequestID, "error", err)
	}

	rp.mu.Lock()
	le, ok := rp.localLimiters[label]
	if !ok {
		le = &localLimiter{limiter: rate.NewLimiter(rate.Limit(rp.config.RateLimitQPS), rp.config.RateLimitBurst)}
		rp.localLimiters[label] = le
	}
	le.lastSeen = rp.now()
	allow := le.limiter.Allow()
	rp.mu.Unlock()

	if !allow {
		observability.RateLimitedTotal.WithLabelValues(label).Inc()
		return &PolicyDecision{
			StatusCode: http.StatusTooManyRequests,
			ErrorCode:  "rate_limit_exceeded",
			Message:    "请求过于频繁，请稍后再试。",
			Reason:     "local rate limit exceeded",
			RetryAfter: retryAfterSeconds(rp.config.RateLimitQPS),
		}
	}
	return nil
}

// QuotaPolicy 每日调用配额策略
type QuotaPolicy struct {
	config *config.Config
	rdb    *redis.Client
}

func (qp *QuotaPolicy) Name() string { return "quota" }
func (qp *QuotaPolicy) Close()       {}
func (qp *QuotaPolicy) Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	if env.APIKey == "" || qp.rdb == nil {
		return nil
	}

	var limit int64
	for _, entry := range qp.config.APIKeys {
		if entry.Key == env.APIKey {
			limit = entry.DailyQuota
			break
		}
	}
	if limit <= 0 {
		return nil
	}

	redisKey := fmt.Sprintf("quota:usage:%s", env.APIKey)
	usageStr, err := qp.rdb.Get(ctx, redisKey).Result()
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

	ttl, _ := qp.rdb.TTL(ctx, redisKey).Result()
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

// InputGuardPolicy 输入内容安全护栏策略
type InputGuardPolicy struct {
	nitroClient        nitro.NitroClient
	intelligenceClient pb.AiLogicClient
	config             *config.Config
}

func (ig *InputGuardPolicy) Name() string { return "input_guard" }
func (ig *InputGuardPolicy) Close()       {}
func (ig *InputGuardPolicy) Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	prompt := env.Prompt

	// Nitro 属于同步安全关键路径，默认按 fail-closed 处理。
	if ig.nitroClient != nil {
		nitroCtx, cancelNitro := context.WithTimeout(ctx, ig.config.GuardrailNitroTimeout)
		defer cancelNitro()

		sanitized, err := ig.nitroClient.CheckInput(nitroCtx, prompt)
		if err != nil {
			if strings.EqualFold(ig.config.NitroFailureMode, "fail_open_with_audit") {
				return &PolicyDecision{
					Allow:           true,
					Degraded:        true,
					DegradeReason:   "nitro input guardrail unavailable",
					SanitizedPrompt: prompt,
				}
			}
			return &PolicyDecision{
				StatusCode: http.StatusServiceUnavailable,
				ErrorCode:  "guardrail_unavailable",
				Message:    "input guardrail unavailable",
				Reason:     "nitro safety check failed",
			}
		}
		prompt = sanitized
	}

	if ig.intelligenceClient == nil {
		return &PolicyDecision{Allow: true, SanitizedPrompt: prompt}
	}

	pyCtx, cancelPy := context.WithTimeout(ctx, ig.config.GuardrailIntellTimeout)
	defer cancelPy()

	// Python 输入护栏属于可配置增强能力，可按 failure mode 决定 fail-open 还是 fail-closed。
	pyResp, err := ig.intelligenceClient.CheckInput(pyCtx, &pb.InputRequest{Prompt: prompt})
	if err != nil {
		if strings.EqualFold(ig.config.PythonInputFailureMode, "fail_closed") {
			return &PolicyDecision{
				StatusCode: http.StatusServiceUnavailable,
				ErrorCode:  "python_guardrail_unavailable",
				Message:    "python input guardrail unavailable",
				Reason:     "python input guardrail failure",
			}
		}
		slog.Warn("python input guardrail degraded", "request_id", env.RequestID, "error", err)
		return &PolicyDecision{
			Allow:           true,
			Degraded:        true,
			DegradeReason:   "python input guardrail unavailable",
			SanitizedPrompt: prompt,
		}
	}

	if !pyResp.Safe {
		return &PolicyDecision{
			StatusCode: http.StatusForbidden,
			ErrorCode:  "security_block",
			Message:    pyResp.Reason,
			Reason:     pyResp.Reason,
		}
	}
	return &PolicyDecision{
		Allow:           true,
		SanitizedPrompt: pyResp.SanitizedPrompt,
	}
}
