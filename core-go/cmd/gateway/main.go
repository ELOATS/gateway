// main 是 AI 网关核心服务的入口点。
package main

import (
	"log"
	"log/slog"

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
	// 1. 加载配置：解析 .env 文件与环境变量。
	cfg := config.LoadConfig()

	// 2. 初始化日志：配置标准化的结构化 JSON 日志输出。
	observability.InitLogger()
	slog.Info("正在初始化 AI 网关核心", "port", cfg.Port)

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
	engine := routes.NewRouter(chatHandler, cfg)

	// 6. 启动服务：
	slog.Info("AI 网关核心服务已就绪",
		"addr", ":"+cfg.Port,
		"keys_loaded", len(cfg.APIKeys),
		"ratelimit_qps", cfg.RateLimitQPS,
	)
	if err := engine.Run(":" + cfg.Port); err != nil {
		log.Fatalf("致命错误: 服务器启动失败: %v", err)
	}
}

// initNodes 根据配置初始化模型节点。
func initNodes(cfg *config.Config) []*router.ModelNode {
	if cfg.OpenAIApiKey != "" && cfg.OpenAIApiKey != "your-openai-api-key-here" {
		slog.Info("已注册 OpenAI 真实适配器", "model_id", "gpt-4")
		return []*router.ModelNode{
			{
				Name:      "OpenAI-主节点",
				ModelID:   "gpt-4",
				Adapter:   adapters.NewOpenAIAdapter(cfg.OpenAIApiKey, cfg.OpenAIURL, cfg.OpenAITimeout),
				Weight:    100,
				CostPer1K: 0.03,
				Quality:   0.95,
				Tags:      map[string]string{"tier": "premium", "provider": "openai"},
				Enabled:   true,
			},
		}
	}

	slog.Warn("未检测到 OpenAI API Key，将使用 Mock 适配器。")
	return []*router.ModelNode{
		{
			Name: "主模拟节点", ModelID: "mock-primary",
			Adapter: &adapters.MockAdapter{Name: "Primary"}, Weight: 80,
			CostPer1K: 0.001, Quality: 0.7,
			Tags: map[string]string{"tier": "standard"}, Enabled: true,
		},
		{
			Name: "备用模拟节点", ModelID: "mock-secondary",
			Adapter: &adapters.MockAdapter{Name: "Secondary"}, Weight: 20,
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
	sr.RegisterStrategy(&router.WeightedStrategy{})
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
