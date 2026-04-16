package cache

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ai-gateway/core/pkg/models"
	"github.com/redis/go-redis/v9"
)

func BenchmarkContextStore_Retrieve(b *testing.B) {
	// Setup a real redis client if reachable, otherwise skip
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		b.Skip("Redis not available on localhost:6379, skipping benchmark")
	}
	defer rdb.Close()

	store := NewContextStore(rdb, 1*time.Hour)
	sessionID := "bench-session"
	store.Clear(ctx, sessionID)
	defer store.Clear(ctx, sessionID)

	// Pre-fill with many messages (e.g., 1000)
	var largeHistory []models.Message
	for i := 0; i < 1000; i++ {
		largeHistory = append(largeHistory, models.Message{
			Role:    "user",
			Content: fmt.Sprintf("Message %d: some lengthy content to simulate real world usage...", i),
		})
	}
	_ = store.Append(ctx, sessionID, largeHistory)

	b.Run("Retrieve_Last_10_from_1000", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			msgs, err := store.Retrieve(ctx, sessionID)
			if err != nil {
				b.Fatal(err)
			}
			if len(msgs) != 10 {
				b.Fatalf("expected 10 messages, got %d", len(msgs))
			}
		}
	})
}
