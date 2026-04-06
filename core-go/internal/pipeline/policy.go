package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/dependencies"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

type Policy interface {
	Name() string
	Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision
	Close()
}

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
		return &InputGuardPolicy{dependencies: deps.Dependencies}, nil
	})
}

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

type inputGuardChecker interface {
	CheckInput(ctx context.Context, prompt string) dependencies.InputGuardOutcome
}

type InputGuardPolicy struct {
	dependencies inputGuardChecker
}

func (ig *InputGuardPolicy) Name() string { return "input_guard" }
func (ig *InputGuardPolicy) Close()       {}
func (ig *InputGuardPolicy) Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	if ig.dependencies == nil {
		return &PolicyDecision{Allow: true, SanitizedPrompt: env.Prompt}
	}

	outcome := ig.dependencies.CheckInput(ctx, env.Prompt)
	if !outcome.Allowed {
		return &PolicyDecision{
			Allow:           false,
			StatusCode:      outcome.StatusCode,
			ErrorCode:       outcome.ErrorCode,
			Message:         outcome.Message,
			Reason:          outcome.Reason,
			SanitizedPrompt: outcome.SanitizedPrompt,
			Degraded:        outcome.Degraded,
			DegradeReason:   outcome.DegradeReason,
		}
	}

	return &PolicyDecision{
		Allow:           true,
		SanitizedPrompt: outcome.SanitizedPrompt,
		Degraded:        outcome.Degraded,
		DegradeReason:   outcome.DegradeReason,
	}
}
