package quota

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

func UpdateUsage(ctx context.Context, rdb *redis.Client, apiKey string, tokens int64) error {
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
