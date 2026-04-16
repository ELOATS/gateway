package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ai-gateway/core/pkg/models"
	"github.com/redis/go-redis/v9"
)

// ContextStore 为无状态 Agent 提供分布式、长时记忆的上下文存储。
// Agent 只要携带相同的 session_id 访问网关，即可复用历史 Context，从而：
// 1. 突破单次请求的 Token 发送上限。
// 2. 将昂贵的长文本状态维护在网关侧。
type ContextStore struct {
	rdb        *redis.Client
	expiration time.Duration
}

// NewContextStore 初始化一个上下文存储引擎。
func NewContextStore(rdb *redis.Client, expiration time.Duration) *ContextStore {
	return &ContextStore{
		rdb:        rdb,
		expiration: expiration,
	}
}

// pushContextKey 根据 Session 生成 Redis 键名。
func pushContextKey(sessionID string) string {
	return fmt.Sprintf("agent:context:%s", sessionID)
}

// Append 拼接新的 Message 到指定 Session 的上下文尾部。
func (s *ContextStore) Append(ctx context.Context, sessionID string, msgs []models.Message) error {
	if len(msgs) == 0 {
		return nil
	}

	key := pushContextKey(sessionID)
	// 序列化后追加
	var values []interface{}
	for _, m := range msgs {
		data, err := json.Marshal(m)
		if err != nil {
			continue
		}
		values = append(values, string(data))
	}

	pipe := s.rdb.Pipeline()
	pipe.RPush(ctx, key, values...)
	pipe.Expire(ctx, key, s.expiration)
	_, err := pipe.Exec(ctx)
	return err
}

// Retrieve 拉取指定 Session 的所有历史对话记录。
func (s *ContextStore) Retrieve(ctx context.Context, sessionID string) ([]models.Message, error) {
	key := pushContextKey(sessionID)

	rawList, err := s.rdb.LRange(ctx, key, -10, -1).Result()
	if err == redis.Nil {
		return []models.Message{}, nil
	} else if err != nil {
		return nil, err
	}

	var messages []models.Message
	for _, raw := range rawList {
		var m models.Message
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			messages = append(messages, m)
		}
	}
	return messages, nil
}

// Clear 主动清空指定的 Session 以释放内存。
func (s *ContextStore) Clear(ctx context.Context, sessionID string) error {
	return s.rdb.Del(ctx, pushContextKey(sessionID)).Err()
}
