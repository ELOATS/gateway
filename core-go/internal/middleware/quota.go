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

// QuotaLimiter 返回一个基于 Redis 的每日消费配额拦截中间件。
// 它要求请求上下文中已经存在 "key_label" 和 "api_key"（由 Auth 中间件设置）。
func QuotaLimiter(rdb *redis.Client, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.GetString("api_key")
		if apiKey == "" {
			c.Next()
			return
		}

		// 1. 查找该 Key 的配置配额
		var limit int64 = 0
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

		// 2. 检查 Redis 中的当前累计值 (不再区分日期，依靠 TTL 滚动)
		redisKey := fmt.Sprintf("quota:usage:%s", apiKey)

		usageStr, err := rdb.Get(c.Request.Context(), redisKey).Result()
		if err == redis.Nil {
			usageStr = "0"
		} else if err != nil {
			c.Next()
			return
		}

		usage, _ := strconv.ParseInt(usageStr, 10, 64)

		// 3. 判定是否超限
		if usage >= limit {
			// 获取 Redis Key 的剩余生存时间 (TTL)
			ttl, _ := rdb.TTL(c.Request.Context(), redisKey).Result()
			if ttl < 0 {
				ttl = 24 * time.Hour // 后备方案
			}

			hours := int(ttl.Hours())
			minutes := int(ttl.Minutes()) % 60

			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":   "quota_exceeded",
				"message": fmt.Sprintf("您的配额已耗尽 (%d/%d Tokens)。将在 %d 小时 %d 分钟后重置。", usage, limit, hours, minutes),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// UpdateQuotaUsage 用于在请求完成后异步更新配额消耗。
func UpdateQuotaUsage(ctx context.Context, rdb *redis.Client, apiKey string, tokens int64) error {
	redisKey := fmt.Sprintf("quota:usage:%s", apiKey)

	// 使用 Lua 脚本确保 Incr 和 Expire(NX) 的原子性，实现固定 24 小时窗口
	script := `
		local current = redis.call("INCRBY", KEYS[1], ARGV[1])
		if current == tonumber(ARGV[1]) then
			redis.call("EXPIRE", KEYS[1], ARGV[2])
		end
		return current
	`
	return rdb.Eval(ctx, script, []string{redisKey}, tokens, 86400).Err()
}
