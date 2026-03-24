package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ai-gateway/core/internal/observability"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

type localLimiter struct {
	l        *rate.Limiter
	lastSeen time.Time
}

// RateLimiter 返回一个 Gin 中间件，支持基于 Redis 的分布式滑动窗口限流。
// 如果 Redis 不可用，将自动降级为本地内存令牌桶限流。
func RateLimiter(rdb *redis.Client, qps float64, burst int) gin.HandlerFunc {
	// 本地降级限流器池（用于 Redis 故障或未配置时）。
	localLimiters := make(map[string]*localLimiter)
	var mu sync.Mutex

	// 后台清理过期限流器
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			mu.Lock()
			now := time.Now()
			for k, v := range localLimiters {
				if now.Sub(v.lastSeen) > time.Hour {
					delete(localLimiters, k)
				}
			}
			mu.Unlock()
		}
	}()

	return func(c *gin.Context) {
		label := c.GetString("key_label")
		if label == "" {
			label = "anonymous"
		}

		// 优先尝试 Redis 分布式限流。
		if rdb != nil {
			allowed, err := checkRedisLimit(c.Request.Context(), rdb, label, qps, burst)
			if err == nil {
				if !allowed {
					reject(c, qps, label)
					return
				}
				c.Next()
				return
			}
			// Redis 故障，降级到本地逻辑。
			slog.Warn("分布式限流异常，降级至本地限流", "label", label, "error", err)
		}

		// 本地限流逻辑。
		mu.Lock()
		le, ok := localLimiters[label]
		if !ok {
			le = &localLimiter{l: rate.NewLimiter(rate.Limit(qps), burst)}
			localLimiters[label] = le
		}
		le.lastSeen = time.Now()
		allow := le.l.Allow()
		mu.Unlock()

		if !allow {
			reject(c, qps, label)
			return
		}
		c.Next()
	}
}

// checkRedisLimit 使用 Redis 脚本实现滑动窗口限流。
func checkRedisLimit(ctx context.Context, rdb *redis.Client, label string, qps float64, burst int) (bool, error) {
	key := "rl:" + label
	now := time.Now().UnixNano() / 1e6 // 毫秒
	window := int64(1000)             // 1秒滑动窗口
	limit := int64(qps)               // 粗略 QPS 限制

	// Lua 脚本实现：1. 清理过期数据 2. 统计计数 3. 判断并写入
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

func reject(c *gin.Context, qps float64, label string) {
	rid, _ := c.Get(RequestIDKey)
	observability.RateLimitedTotal.WithLabelValues(label).Inc()

	c.JSON(http.StatusTooManyRequests, gin.H{
		"error":      "rate_limit_exceeded",
		"message":    "请求过于频繁，请稍后再试。",
		"request_id": rid,
	})

	c.Header("Retry-After", strconv.Itoa(int(time.Second/time.Duration(qps))))
	c.Abort()
}
