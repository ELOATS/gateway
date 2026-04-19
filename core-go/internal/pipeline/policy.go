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
	"github.com/ai-gateway/core/internal/db"
	"github.com/ai-gateway/core/internal/dependencies"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// Policy 定义了网关请求切面的通用策略接口。
// 每一个策略都可以对请求进行评估、拦截、降级或内容脱敏。
type Policy interface {
	Name() string                                                       // 获取策略的唯一标识名
	Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision // 执行具体的策略逻辑并返回决策
	Close()                                                             // 优雅关闭策略实例，释放其持有的资源（如连接池、计时器）
}

// RegisterPolicies 负责将所有内置策略类型注册到策略工厂中。
// 只有在这里注册过的策略才能在 policies.yaml 配置文件中通过名称被引用。
func RegisterPolicies() {
	// tool_auth: 约束 API Key 对 Agent 调试工具的访问权限
	RegisterPolicy("tool_auth", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		return &ToolAuthPolicy{}, nil
	})

	// rate_limit: 分布式/本地限流策略
	RegisterPolicy("rate_limit", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		qps := deps.Config.RateLimitQPS
		burst := deps.Config.RateLimitBurst

		if q, ok := cfg["qps"].(int); ok {
			qps = float64(q)
		} else if qf, ok := cfg["qps"].(float64); ok {
			qps = qf
		}
		if b, ok := cfg["burst"].(int); ok {
			burst = b
		} else if bf, ok := cfg["burst"].(float64); ok {
			burst = int(bf)
		}

		return NewRateLimitPolicy(qps, burst, deps.RedisClient, deps.TenantManager), nil
	})

	// quota: 每日 Token 配额检查
	RegisterPolicy("quota", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		return &QuotaPolicy{config: deps.Config, rdb: deps.RedisClient, tm: deps.TenantManager}, nil
	})

	// input_guard: 基于 Nitro/Intelligence 的输入内容安全审计
	RegisterPolicy("input_guard", func(deps *DependencyContainer, cfg map[string]any) (Policy, error) {
		return &InputGuardPolicy{dependencies: deps.Dependencies}, nil
	})
}

// ToolAuthPolicy 实现工具权限审计。
// 它确保非 Premium/Admin 层级的 API Key 无法进行复杂的 Tool Call 或 Function Calling。
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

// RateLimitPolicy 提供高可用的限流能力。
// 它采用两级限流：优先尝试基于 Redis 的分布式限流，若 Redis 异常则降级为 Go 内存中的本地令牌桶。
type RateLimitPolicy struct {
	defaultQPS    float64                  // 默认每秒请求数限制
	defaultBurst  int                      // 默认并发桶大小
	rdb           *redis.Client            // 分布式限流使用的 Redis 客户端
	tm            db.TenantManager         // 租户管理器，用于从数据库实时同步租户限流配置
	mu            sync.Mutex               // 保护本地限流器映射表
	localLimiters map[string]*localLimiter // 租户标号与本地令牌桶的映射
	now           func() time.Time         // Mock 用当前时间函数
	cancel        context.CancelFunc       // 用于停止本地限流器清理任务
}

func NewRateLimitPolicy(qps float64, burst int, rdb *redis.Client, tm db.TenantManager) *RateLimitPolicy {
	ctx, cancel := context.WithCancel(context.Background())
	rp := &RateLimitPolicy{
		defaultQPS:    qps,
		defaultBurst:  burst,
		rdb:           rdb,
		tm:            tm,
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
	qps := rp.defaultQPS
	burst := rp.defaultBurst

	if rp.tm != nil && env.TenantID > 0 {
		if tenant, err := rp.tm.GetTenantByID(env.TenantID); err == nil {
			for _, q := range tenant.Quotas {
				if q.LimitType == "request" && q.TimeWindow == "second" {
					qps = float64(q.Value)
					burst = int(qps * 2) // Default burst to 2x QPS
					break
				}
			}
		}
	}

	if qps <= 0 || burst <= 0 {
		return nil
	}

	label := env.KeyLabel
	if label == "" {
		label = "anonymous"
	}

	if rp.rdb != nil {
		allowed, err := checkRedisRateLimit(ctx, rp.rdb, label, qps, burst)
		if err == nil {
			if !allowed {
				observability.RateLimitedTotal.WithLabelValues(label).Inc()
				return &PolicyDecision{
					StatusCode: http.StatusTooManyRequests,
					ErrorCode:  "rate_limit_exceeded",
					Message:    "请求过于频繁，请稍后再试。",
					Reason:     "distributed rate limit exceeded",
					RetryAfter: retryAfterSeconds(qps),
				}
			}
			return nil
		}
		slog.Warn("distributed rate limit degraded to local limiter", "request_id", env.RequestID, "error", err)
	}

	rp.mu.Lock()
	le, ok := rp.localLimiters[label]
	if !ok || le.limiter.Limit() != rate.Limit(qps) {
		le = &localLimiter{limiter: rate.NewLimiter(rate.Limit(qps), burst)}
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
			RetryAfter: retryAfterSeconds(qps),
		}
	}
	return nil
}

// QuotaPolicy 实现每日 Token 消耗配额限制。
// 它通过 Redis 记录租户或 API Key 的当日累计消耗量，并与设定的配额进行实时对比。
type QuotaPolicy struct {
	config *config.Config   // 初始化配额偏好配置
	rdb    *redis.Client    // 用于存储配额记录的 Redis 客户端
	tm     db.TenantManager // 租户管理器，用于获取动态配额定义
}

func (qp *QuotaPolicy) Name() string { return "quota" }
func (qp *QuotaPolicy) Close()       {}
func (qp *QuotaPolicy) Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	if env.APIKey == "" || qp.rdb == nil {
		return nil
	}

	var limit int64
	if qp.tm != nil && env.TenantID > 0 {
		tenant, err := qp.tm.GetTenantByID(env.TenantID)
		if err == nil {
			for _, q := range tenant.Quotas {
				if q.LimitType == "token" && q.TimeWindow == "daily" {
					limit = q.Value
					break
				}
			}
		}
	} else {
		for _, entry := range qp.config.APIKeys {
			if entry.Key == env.APIKey {
				limit = entry.DailyQuota
				break
			}
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

// InputGuardPolicy 负责请求端的安全护栏审计（输入审计）。
// 它是防止 Prompt Injection 和 PII 泄露的第一道防线。
type InputGuardPolicy struct {
	dependencies inputGuardChecker // 注入输入审计相关的后端服务接口
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
