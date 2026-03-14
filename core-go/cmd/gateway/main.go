// main 是 AI 网关核心服务的入口点。
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
	// 1. 加载配置：解析 .env 文件与环境变量。
	cfg := config.LoadConfig()

	// 2. 初始化日志：配置标准化的结构化 JSON 日志输出。
	observability.InitLogger()
	slog.Info("正在初始化 AI 网关核心", "port", cfg.Port)

	// gRPC 连接选项：配置不安全凭据（内部网络使用）与自适应补偿重试（Backoff）。
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay: 1 * time.Second,
				MaxDelay:  10 * time.Second,
			},
		}),
	}

	// 3. 建立 gRPC 连接：
	// intelligenceClient 用于连接 Python 驱动的智能调度/审计平面。
	intelligenceClient, pyConn := mustDial(cfg.PythonAddr, dialOpts...)
	defer pyConn.Close()

	// nitroClient 用于连接 Rust 驱动的高性能分词/加速平面。
	nitroClient, rsConn := mustDial(cfg.RustAddr, dialOpts...)
	defer rsConn.Close()

	// 4. 构建路由表：
	// 动态构建候选节点：如果配置了外部 OpenAI API Key，则接入真实 OpenAI；否则回退到 Mock 适配器以供本地测试。
	var candidates []router.Candidate
	if cfg.OpenAIApiKey != "" && cfg.OpenAIApiKey != "your-openai-api-key-here" {
		candidates = append(candidates, router.Candidate{
			Name:    "OpenAI-主节点",
			Weight:  100,
			Adapter: adapters.NewOpenAIAdapter(cfg.OpenAIApiKey),
		})
		slog.Info("已注册 OpenAI 真实适配器")
	} else {
		candidates = []router.Candidate{
			{Name: "主模拟节点", Weight: 80, Adapter: &adapters.MockAdapter{Name: "Primary"}},
			{Name: "备用模拟节点", Weight: 20, Adapter: &adapters.MockAdapter{Name: "Secondary"}},
		}
		slog.Warn("未检测到 OpenAI API Key，将为所有节点使用 Mock 适配器。")
	}

	// 5. 初始化业务组件：
	// sr 提供基于权重的随机路由能力。
	sr := router.NewSmartRouter(candidates)
	// chatHandler 整合了三层控制面的业务逻辑。
	chatHandler := handlers.NewChatHandler(intelligenceClient, nitroClient, sr)

	// 6. 配置 HTTP 路由：整合 Prometheus 监控、健康检查与 V1 API 路由。
	engine := routes.NewRouter(chatHandler, cfg)

	// 7. 启动服务：开始监听指定的管理端口。
	slog.Info("AI 网关核心服务已就绪", 
		"addr", ":"+cfg.Port, 
		"keys_loaded", len(cfg.APIKeys),
		"ratelimit_qps", cfg.RateLimitQPS,
	)
	if err := engine.Run(":" + cfg.Port); err != nil {
		log.Fatalf("致命错误: 服务器启动失败: %v", err)
	}
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
