// main 是 AI 网关核心服务的入口点。
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/ai-gateway/core/api/gateway/v1"
	"github.com/ai-gateway/core/internal/adapters"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/handlers"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/router"
	"github.com/ai-gateway/core/internal/routes"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	// 1. 加载配置：解析 .env 文件与环境变量。
	cfg := config.LoadConfig()

	// 2. 初始化日志：配置标准化的结构化 JSON 日志输出。
	observability.InitLogger()
	slog.Info("正在初始化 AI 网关核心", "port", cfg.Port)

	// 初始化 OpenTelemetry 追踪。
	shutdownTracer, err := observability.InitTracer(context.Background(), cfg.OTELCollectorAddr)
	if err != nil {
		slog.Warn("无法初始化追踪器", "error", err)
	}
	defer shutdownTracer()

	// 初始化 Redis 客户端。
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	// 验证连接。
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		slog.Warn("无法连接至 Redis，分布式限流将不可用", "error", err)
	} else {
		slog.Info("Redis 连接成功", "addr", cfg.RedisAddr)
	}
	defer rdb.Close()

	// gRPC 连接选项：配置不安全凭据与自适应补偿重试。
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay: cfg.GRPCBaseDelay,
				MaxDelay:  cfg.GRPCMaxDelay,
			},
		}),
	}

	// 3. 建立 gRPC 连接：
	intelligenceClient, pyConn := mustDial(cfg.PythonAddr, dialOpts...)
	defer pyConn.Close()

	nitroClient, rsConn := mustDial(cfg.RustAddr, dialOpts...)
	defer rsConn.Close()

	// 4. 初始化核心路由组件：
	nodes := initNodes(cfg)
	sr, _ := initSmartRouter(cfg, nodes)

	slog.Info("智能路由引擎就绪",
		"default_strategy", cfg.RouteStrategy,
		"nodes_registered", len(nodes),
		"strategies_registered", 6,
	)

	// 5. 初始化业务组件与 HTTP 服务：
	chatHandler := handlers.NewChatHandler(intelligenceClient, nitroClient, sr, cfg)
	adminHandler := handlers.NewAdminHandler(sr)
	routerEngine := routes.NewRouter(chatHandler, adminHandler, rdb, cfg)

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: routerEngine,
	}

	// 6. 启动服务（在 goroutine 中）：
	go func() {
		slog.Info("AI 网关核心服务已就绪",
			"addr", srv.Addr,
			"keys_loaded", len(cfg.APIKeys),
			"ratelimit_qps", cfg.RateLimitQPS,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("致命错误: 服务器异常退出: %v", err)
		}
	}()

	// 7. 优雅关停：监听中断信号。
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("正在关闭服务器...")

	// 设定关停超时。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("服务器强制关闭", "error", err)
	}

	slog.Info("服务器已退出")
}

// initNodes 根据配置初始化模型节点。
func initNodes(cfg *config.Config) []*router.ModelNode {
	if cfg.OpenAIApiKey != "" && cfg.OpenAIApiKey != "your-openai-api-key-here" {
		slog.Info("已注册 OpenAI 真实适配器", "model_id", "gpt-4")
		adapter, _ := adapters.NewProvider(adapters.Config{
			Type:    adapters.OpenAI,
			APIKey:  cfg.OpenAIApiKey,
			URL:     cfg.OpenAIURL,
			Timeout: cfg.OpenAITimeout,
		})
		return []*router.ModelNode{
			{
				Name:      "OpenAI-主节点",
				ModelID:   "gpt-4",
				Adapter:   adapter,
				Weight:    100,
				CostPer1K: 0.03,
				Quality:   0.95,
				Tags:      map[string]string{"tier": "premium", "provider": "openai"},
				Enabled:   true,
			},
		}
	}

	slog.Warn("未检测到 OpenAI API Key，将使用 Mock 适配器。")
	mock1, _ := adapters.NewProvider(adapters.Config{Type: adapters.Mock, Name: "Primary"})
	mock2, _ := adapters.NewProvider(adapters.Config{Type: adapters.Mock, Name: "Secondary"})

	return []*router.ModelNode{
		{
			Name: "主模拟节点", ModelID: "mock-primary",
			Adapter: mock1, Weight: 80,
			CostPer1K: 0.001, Quality: 0.7,
			Tags: map[string]string{"tier": "standard"}, Enabled: true,
		},
		{
			Name: "备用模拟节点", ModelID: "mock-secondary",
			Adapter: mock2, Weight: 20,
			CostPer1K: 0.0005, Quality: 0.5,
			Tags: map[string]string{"tier": "economy"}, Enabled: true,
		},
	}
}

// initSmartRouter 初始化路由器并注册所有路由策略。
func initSmartRouter(cfg *config.Config, nodes []*router.ModelNode) (*router.SmartRouter, *router.HealthTracker) {
	tracker := router.NewHealthTracker(cfg.HealthAlpha)
	sr := router.NewSmartRouter(nodes, tracker, cfg.RouteStrategy)

	// 注册所有路由策略：
	sr.RegisterStrategy(&router.WeightedStrategy{Tracker: tracker})
	sr.RegisterStrategy(&router.CostStrategy{MinQuality: 0.6})
	sr.RegisterStrategy(&router.LatencyStrategy{Tracker: tracker})
	sr.RegisterStrategy(&router.QualityStrategy{Tracker: tracker})
	sr.RegisterStrategy(&router.FallbackStrategy{Tracker: tracker})

	// 规则路由：示例规则（按需自定义）。
	sr.RegisterStrategy(router.NewRuleStrategy([]router.Rule{
		{
			Name:     "VIP 用户路由到 Premium 节点",
			Priority: 1,
			Target:   "OpenAI-主节点",
			Condition: func(ctx *router.RouteContext) bool {
				return ctx.UserTier == "admin" || ctx.UserTier == "vip"
			},
		},
	}))

	return sr, tracker
}

// mustDial 建立与目标地址的 gRPC 连接。
// 该方法会同步等待连接基础架构初始化，并在连接失败时触发程序强制退出以符合云原生 Fail-Fast 原则。
func mustDial(addr string, opts ...grpc.DialOption) (pb.AiLogicClient, *grpc.ClientConn) {
	conn, err := grpc.Dial(addr, opts...)
	if err != nil {
		slog.Error("gRPC 连接建立失败", "target", addr, "error", err)
		log.Fatalf("致命错误: 无法连接至后端服务 %s: %v", addr, err)
	}
	return pb.NewAiLogicClient(conn), conn
}
