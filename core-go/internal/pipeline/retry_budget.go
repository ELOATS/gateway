package pipeline

import (
	"context"

	"golang.org/x/time/rate"
)

// RetryBudgetPolicy 维护一个全局的重试预算，用于精细化控制全系统的重试开销。
// 此策略使用令牌桶（Token Bucket）算法实现。只有在获得可用令牌时才允许执行重试。
// 这样做的核心目的是防止在下游系统故障时产生“重试风暴”（Retry Storm），避免压垮已经处于脆弱状态的服务。
type RetryBudgetPolicy struct {
	limiter *rate.Limiter // 内部限流器，用于控制令牌发放
}

// NewRetryBudgetPolicy 创建一个新的重试预算策略实例。
// ratePerSecond: 每秒恢复的重试额度（令牌数）。注：这不仅是 QPS，而是系统层面对故障恢复能力的宽容度。
// burst: 允许瞬间爆发的重试峰值容量。
func NewRetryBudgetPolicy(ratePerSecond float64, burst int) *RetryBudgetPolicy {
	// 防御性检查：如果配置无效，则使用安全的默认保守配置
	if ratePerSecond <= 0 {
		ratePerSecond = 100 // 默认值：每秒 100 次重试额度
	}
	if burst <= 0 {
		burst = 50 // 默认值：允许 50 个令牌爆发
	}
	return &RetryBudgetPolicy{
		limiter: rate.NewLimiter(rate.Limit(ratePerSecond), burst),
	}
}

// AcquireRetry 尝试从预算池中获取一个重试令牌。
// 如果返回 false，表示重试额度已耗尽，此时 Pipeline 必须直接向用户返回错误，禁止进一步重试。
func (r *RetryBudgetPolicy) AcquireRetry() bool {
	return r.limiter.Allow()
}

// Name returns the policy name.
func (r *RetryBudgetPolicy) Name() string {
	return "retry_budget"
}

// Evaluate 实现了 Policy 接口。
// 注：虽然它可以集成在策略链中，但在 Pipeline 的重试循环内部，更推荐直接调用 AcquireRetry 以获得即时反馈。
func (r *RetryBudgetPolicy) Evaluate(ctx context.Context, env *RequestEnvelope) *PolicyDecision {
	return nil
}

// Close complies with the Policy interface.
func (r *RetryBudgetPolicy) Close() {}
