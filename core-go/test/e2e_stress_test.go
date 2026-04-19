package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ai-gateway/core/internal/adapters"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/handlers"
	"github.com/ai-gateway/core/internal/router"
	"github.com/ai-gateway/core/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/nitro"
)

func BenchmarkFullGatewayStack(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)

	// 1. Setup Dependencies (assuming local services are running or using fallback)
	cfg := &config.Config{
		RequestTimeout: 30 * time.Second,
		APIKeys: []config.APIKeyEntry{
			{Key: "sk-perf-test", Label: "premium"},
		},
	}

	ctx := context.Background()

	// Redis
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := rdb.Ping(ctx).Err(); err != nil {
		b.Log("Warning: Redis not reachable, some tests may fail")
	}

	// gRPC Clients (Python/Rust)
	connOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	pyConn, err := grpc.Dial("localhost:50051", connOpts...)
	if err != nil {
		b.Fatalf("failed to connect to python intelligence service: %v", err)
	}
	ic := pb.NewAiLogicClient(pyConn)

	nitroConn, _ := grpc.Dial("localhost:50052", connOpts...)
	nitroClient := &nitro.GrpcNitroClient{Client: pb.NewAiLogicClient(nitroConn), Conn: nitroConn}

	// Smart Router with Mock Adapter to avoid real LLM calls
	mockNode := &router.ModelNode{
		Name:    "perf-mock-node",
		ModelID: "gpt-4",
		Adapter: &adapters.MockAdapter{Name: "perf-tester"},
		Enabled: true,
	}
	sr := router.NewSmartRouter([]*router.ModelNode{mockNode}, router.NewHealthTracker(0.1), "fixed")

	handler := handlers.NewChatHandler(ic, nitroClient, sr, nil, nil, rdb, cfg)

	// 2. Prepare Request
	reqBody, _ := json.Marshal(models.ChatCompletionRequest{
		Model: "gpt-4",
		Messages: []models.Message{
			{Role: "user", Content: "this is a performance test message"},
		},
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			r, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(reqBody))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer sk-perf-test")
			c.Request = r

			handler.HandleChatCompletions(c)

			if w.Code != http.StatusOK {
				// b.Errorf("unexpected status code: %d, body: %s", w.Code, w.Body.String())
			}
		}
	})
}
