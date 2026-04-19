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
	"github.com/ai-gateway/core/internal/application/billing"
	"github.com/ai-gateway/core/internal/application/rerank"
	"github.com/ai-gateway/core/internal/config"
	"github.com/ai-gateway/core/internal/db"
	"github.com/ai-gateway/core/internal/handlers"
	"github.com/ai-gateway/core/internal/nitro"
	"github.com/ai-gateway/core/internal/observability"
	"github.com/ai-gateway/core/internal/router"
	"github.com/ai-gateway/core/internal/routes"
	"github.com/ai-gateway/core/internal/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
)

func main() {
	cfg := mustLoadConfig()

	// 1. 初始化基础架构
	observability.InitLogger()
	slog.Info("正在初始化 AI 网关核心服务", "port", cfg.Port)

	if err := db.InitDB(cfg); err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}

	tm := db.NewTenantManager(db.GlobalDB)
	ce := db.NewCostEngine(db.GlobalDB)

	// 2. 环境探测与插件加载
	status := InitRuntimeStatus(cfg)
	LoadDynamicPlugins(cfg.Paths.AdapterDir)
	shutdownTracer := InitObservability(cfg) // 初始化链路追踪与审计日志
	defer shutdownTracer()

	// 3. 多语言计算服务（Sidcar/Remote）连接建立
	rdb := InitRedis(cfg, status)
	defer rdb.Close()

	dialOpts := mustBuildGRPCDialOptions(cfg)
	intelligenceClient, pyConn := mustDial(cfg.PythonAddr, dialOpts...)
	defer pyConn.Close()
	status.Set(runtime.DependencyStatus{Name: "python", Required: false, Healthy: true, Status: "ready", Version: cfg.PythonAddr, FailureMode: cfg.PythonInputFailureMode})

	// 4. 安全引擎（Nitro）初始化：优先尝试本地 WASM 以获得极致性能，失败则降级为 gRPC 远程调用。
	nitroClient, nitroVersion := initNitro(cfg, dialOpts, status)
	defer nitroClient.Close()

	// 5. 组装核心路由引擎与策略链
	sr, tracker := initSmartRouter(cfg, initNodes(cfg))
	if tracker != nil {
		defer tracker.Close()
	}

	// 6. 依赖注入：将所有底层组件封装进 Handler 层
	chatHandler := handlers.NewChatHandler(intelligenceClient, nitroClient, sr, tm, ce, rdb, cfg)
	adminHandler := handlers.NewAdminHandler(sr, rdb, status)
	tenantHandler := handlers.NewTenantAdminHandler(tm, ce)
	billingService := billing.NewBillingService(db.GlobalDB)
	billingHandler := handlers.NewBillingHandler(billingService)

	// 7. Rerank 外部服务初始化
	rerankProviders := make(map[string]adapters.RerankProvider)
	if cfg.CohereApiKey != "" {
		rerankProviders["cohere-rerank-v3-english"] = adapters.NewRerankAdapter(&adapters.CohereRerankProtocol{}, cfg.CohereApiKey, cfg.CohereURL)
	}
	if cfg.JinaApiKey != "" {
		rerankProviders["jina-reranker-v2-base-multilingual"] = adapters.NewRerankAdapter(&adapters.JinaRerankProtocol{}, cfg.JinaApiKey, cfg.JinaURL)
	}
	rerankService := rerank.NewService(rerankProviders)
	rerankHandler := handlers.NewRerankHandler(rerankService)

	// 8. 启动 HTTP Server 并监听优雅关闭信号
	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: routes.NewRouter(chatHandler, adminHandler, tenantHandler, billingHandler, rerankHandler, tm, rdb, cfg, status),
	}

	runHTTPServer(srv, cfg, nitroVersion)
	waitForShutdown(srv)
}

func mustLoadConfig() *config.Config {
	cfg := config.LoadConfig()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("fatal configuration error: %v", err)
	}
	return cfg
}

func InitObservability(cfg *config.Config) func() {
	shutdownTracer, err := observability.InitTracer(context.Background(), cfg.OTELCollectorAddr)
	if err != nil {
		slog.Warn("failed to initialize tracer", "error", err)
	}

	if err := observability.InitGlobalAuditLogger(cfg.Paths.AuditLogFile); err != nil {
		slog.Warn("failed to initialize audit logger", "error", err)
	} else {
		slog.Info("audit logger initialized", "path", cfg.Paths.AuditLogFile)
	}

	return shutdownTracer
}

func mustBuildGRPCDialOptions(cfg *config.Config) []grpc.DialOption {
	transportCredentials, err := BuildGRPCTransportCredentials(cfg)
	if err != nil {
		log.Fatalf("fatal grpc transport setup error: %v", err)
	}

	return []grpc.DialOption{
		grpc.WithTransportCredentials(transportCredentials),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff: backoff.Config{
				BaseDelay: cfg.GRPCBaseDelay,
				MaxDelay:  cfg.GRPCMaxDelay,
			},
		}),
	}
}

func runHTTPServer(srv *http.Server, cfg *config.Config, nitroVersion string) {
	go func() {
		slog.Info("ai gateway core ready",
			"addr", srv.Addr,
			"keys_loaded", len(cfg.APIKeys),
			"ratelimit_qps", cfg.RateLimitQPS,
			"nitro_backend", nitroVersion,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("fatal server error: %v", err)
		}
	}()
}

func waitForShutdown(srv *http.Server) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down gateway")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("server forced to close", "error", err)
	}

	if observability.GlobalAuditLogger != nil {
		_ = observability.GlobalAuditLogger.Close(ctx)
	}
}

// initNitro 尝试初始化 Nitro 安全护栏执行引擎。
// 它优先尝试加载本地 WASM 运行时，这样可以减少一次网络往返耗时，并将护栏延迟降至 1ms 以内。
// 如果本地环境（WASM 文件缺失或不支持）无法满足，系统会自动回退到远程 gRPC 调用模式（Rust Utils 服务）。
func initNitro(cfg *config.Config, dialOpts []grpc.DialOption, status *runtime.SystemStatus) (nitro.NitroClient, string) {
	wasmPath := cfg.Paths.NitroWasmFile
	if _, err := os.Stat(wasmPath); err == nil {
		rules, _ := os.ReadFile(cfg.Paths.SensitiveRulesFile)
		client, wasmErr := nitro.NewWasmNitroClient(context.Background(), wasmPath, string(rules))
		if wasmErr == nil {
			status.Set(runtime.DependencyStatus{Name: "nitro", Required: true, Healthy: true, Status: "ready", Version: "wasm", FailureMode: cfg.NitroFailureMode})
			return client, "wasm"
		}

		slog.Warn("WASM 模式初始化失败，自动降级为 gRPC 模式", "error", wasmErr)
		status.Set(runtime.DependencyStatus{Name: "nitro", Required: true, Healthy: false, Status: "degraded", Reason: wasmErr.Error(), Version: "wasm", FailureMode: cfg.NitroFailureMode})
	}

	rsClient, rsConn := mustDial(cfg.RustAddr, dialOpts...)
	status.Set(runtime.DependencyStatus{Name: "nitro", Required: true, Healthy: true, Status: "ready", Version: "grpc:" + cfg.RustAddr, FailureMode: cfg.NitroFailureMode})
	return &nitro.GrpcNitroClient{Client: rsClient, Conn: rsConn}, "grpc"
}

func initNodes(cfg *config.Config) []*router.ModelNode {
	if cfg.OpenAIApiKey != "" && cfg.OpenAIApiKey != "your-openai-api-key-here" {
		slog.Info("registering openai adapter", "model_id", "gpt-4")
		adapter, _ := adapters.NewProvider(adapters.Config{
			Type:    adapters.OpenAI,
			APIKey:  cfg.OpenAIApiKey,
			URL:     cfg.OpenAIURL,
			Timeout: cfg.OpenAITimeout,
		})
		return []*router.ModelNode{{
			Name:      "openai-primary",
			ModelID:   "gpt-4",
			Adapter:   adapter,
			Weight:    100,
			CostPer1K: 0.03,
			Quality:   0.95,
			Tags:      map[string]string{"tier": "premium", "provider": "openai"},
			Enabled:   true,
		}}
	}

	slog.Warn("openai api key missing; using mock adapters")
	mock1, _ := adapters.NewProvider(adapters.Config{Type: adapters.Mock, Name: "Primary"})
	mock2, _ := adapters.NewProvider(adapters.Config{Type: adapters.Mock, Name: "Secondary"})

	return []*router.ModelNode{
		{
			Name:      "mock-primary",
			ModelID:   "mock-primary",
			Adapter:   mock1,
			Weight:    80,
			CostPer1K: 0.001,
			Quality:   0.7,
			Tags:      map[string]string{"tier": "standard"},
			Enabled:   true,
		},
		{
			Name:      "mock-secondary",
			ModelID:   "mock-secondary",
			Adapter:   mock2,
			Weight:    20,
			CostPer1K: 0.0005,
			Quality:   0.5,
			Tags:      map[string]string{"tier": "economy"},
			Enabled:   true,
		},
	}
}

func initSmartRouter(cfg *config.Config, nodes []*router.ModelNode) (*router.SmartRouter, *router.HealthTracker) {
	tracker := router.NewHealthTracker(cfg.HealthAlpha)
	sr := router.NewSmartRouter(nodes, tracker, cfg.RouteStrategy)

	sr.RegisterStrategy(&router.WeightedStrategy{Tracker: tracker})
	sr.RegisterStrategy(&router.CostStrategy{MinQuality: 0.6})
	sr.RegisterStrategy(&router.LatencyStrategy{Tracker: tracker})
	sr.RegisterStrategy(&router.QualityStrategy{Tracker: tracker})
	sr.RegisterStrategy(&router.FallbackStrategy{Tracker: tracker})

	sr.RegisterStrategy(router.NewRuleStrategy([]router.Rule{{
		Name:     "vip-users-to-premium-node",
		Priority: 1,
		Target:   "openai-primary",
		Condition: func(ctx *router.RouteContext) bool {
			return ctx.UserTier == "admin" || ctx.UserTier == "vip"
		},
	}}))

	return sr, tracker
}

func mustDial(addr string, opts ...grpc.DialOption) (pb.AiLogicClient, *grpc.ClientConn) {
	conn, err := grpc.Dial(addr, opts...)
	if err != nil {
		slog.Error("grpc dial failed", "target", addr, "error", err)
		log.Fatalf("fatal dial error for %s: %v", addr, err)
	}
	return pb.NewAiLogicClient(conn), conn
}
