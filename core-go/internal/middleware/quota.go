// Deprecated: Quota logic has been moved to internal/pipeline/policy.go.
// This file will be removed in V2 Phase 2.
package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-gateway/core/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// QuotaLimiter 在请求进入执行阶段前检查租户的滚动 24 小时配额。
// 它依赖 Auth 中间件提前写入 api_key，并把真正的扣账放到请求完成后的补偿路径。
func QuotaLimiter(rdb *redis.Client, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetString("api_key")
		if apiKey == "" {
			c.Next()
			return
		}

		// 先从配置中查出该 key 的每日额度。
		var limit int64
		for _, entry := range cfg.APIKeys {
			if entry.Key == apiKey {
				limit = entry.DailyQuota
				break
			}
		}

		if limit <= 0 {
			c.Next()
			return
		}

		// 使用 Redis 中的滚动累计值做硬拦截。
		redisKey := fmt.Sprintf("quota:usage:%s", apiKey)
		usageStr, err := rdb.Get(c.Request.Context(), redisKey).Result()
		if err == redis.Nil {
			usageStr = "0"
		} else if err != nil {
			// Redis 异常时不在这里直接拒绝，由主链路按既定降级策略处理。
			c.Next()
			return
		}

		usage, _ := strconv.ParseInt(usageStr, 10, 64)
		if usage >= limit {
			ttl, _ := rdb.TTL(c.Request.Context(), redisKey).Result()
			if ttl < 0 {
				ttl = 24 * time.Hour
			}

			hours := int(ttl.Hours())
			minutes := int(ttl.Minutes()) % 60

			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":   "quota_exceeded",
				"message": fmt.Sprintf("您的配额已耗尽（%d/%d Tokens），将在 %d 小时 %d 分钟后重置。", usage, limit, hours, minutes),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// UpdateQuotaUsage 在请求完成后异步回写实际消耗。
// Lua 脚本保证 INCRBY 和首次 EXPIRE 的原子性，避免并发下窗口错乱。
func UpdateQuotaUsage(ctx context.Context, rdb *redis.Client, apiKey string, tokens int64) error {
	redisKey := fmt.Sprintf("quota:usage:%s", apiKey)

	script := `
		local current = redis.call("INCRBY", KEYS[1], ARGV[1])
		if current == tonumber(ARGV[1]) then
			redis.call("EXPIRE", KEYS[1], ARGV[2])
		end
		return current
	`
	return rdb.Eval(ctx, script, []string{redisKey}, tokens, 86400).Err()
}
