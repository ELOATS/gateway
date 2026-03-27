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

// RateLimiter 返回一个 Gin 中间件，优先使用 Redis 做分布式限流。
// 当 Redis 不可用时，会自动退化到进程内令牌桶，保证保护措施仍然存在。
func RateLimiter(rdb *redis.Client, qps float64, burst int) gin.HandlerFunc {
	localLimiters := make(map[string]*localLimiter)
	var mu sync.Mutex

	// 本地限流器只在降级路径使用，因此定期回收长期不活跃的租户状态即可。
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

			slog.Warn("分布式限流异常，降级到本地限流", "label", label, "error", err)
		}

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

// checkRedisLimit 用 Redis 有序集合近似实现 1 秒滑动窗口限流。
func checkRedisLimit(ctx context.Context, rdb *redis.Client, label string, qps float64, burst int) (bool, error) {
	key := "rl:" + label
	now := time.Now().UnixNano() / 1e6
	window := int64(1000)
	limit := int64(qps)
	if burst > 0 && int64(burst) > limit {
		limit = int64(burst)
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
