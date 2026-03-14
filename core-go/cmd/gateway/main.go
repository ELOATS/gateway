// Main entry point for the AI Gateway Core.
package main

import (
	"log"
	"log/slog"
	"time"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/adapters"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/handlers"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/router"
	"github.com/ai-gateway/core/internal/routes"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	cfg := config.LoadConfig()
	observability.InitLogger()
	slog.Info("Initializing AI Gateway Core", "port", cfg.Port)

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay: 1 * time.Second,
				MaxDelay:  10 * time.Second,
			},
		}),
	}

	intelligenceClient, pyConn := mustDial(cfg.PythonAddr, dialOpts...)
	defer pyConn.Close()

	nitroClient, rsConn := mustDial(cfg.RustAddr, dialOpts...)
	defer rsConn.Close()

	// 动态构建候选节点：如果配置了 API Key，则接入真实 OpenAI；否则使用 Mock。
	var candidates []router.Candidate
	if cfg.OpenAIApiKey != "" && cfg.OpenAIApiKey != "your-openai-api-key-here" {
		candidates = append(candidates, router.Candidate{
			Name:    "OpenAI-Main",
			Weight:  100,
			Adapter: adapters.NewOpenAIAdapter(cfg.OpenAIApiKey),
		})
		slog.Info("Registered OpenAI real adapter")
	} else {
		candidates = []router.Candidate{
			{Name: "Primary-Mock", Weight: 80, Adapter: &adapters.MockAdapter{Name: "Primary"}},
			{Name: "Secondary-Mock", Weight: 20, Adapter: &adapters.MockAdapter{Name: "Secondary"}},
		}
		slog.Warn("No OpenAI API key found. Using Mock adapters for all nodes.")
	}

	sr := router.NewSmartRouter(candidates)
	chatHandler := handlers.NewChatHandler(intelligenceClient, nitroClient, sr)

	engine := routes.NewRouter(chatHandler, cfg)

	slog.Info("AI Gateway Core is ready", 
		"addr", ":"+cfg.Port, 
		"keys_loaded", len(cfg.APIKeys),
		"ratelimit_qps", cfg.RateLimitQPS,
	)
	if err := engine.Run(":" + cfg.Port); err != nil {
		log.Fatalf("Fatal: server failed: %v", err)
	}
}

func mustDial(addr string, opts ...grpc.DialOption) (pb.AiLogicClient, *grpc.ClientConn) {
	conn, err := grpc.Dial(addr, opts...)
	if err != nil {
		log.Fatalf("Fatal: connection failed to %s: %v", addr, err)
	}
	return pb.NewAiLogicClient(conn), conn
}
