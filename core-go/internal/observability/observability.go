// Package observability 提供监控指标、请求追踪上下文和结构化日志处理。
package observability

import (
	"context"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"google.golang.org/grpc/metadata"
)

// InitTracer 初始化 OpenTelemetry 追踪器。
func InitTracer(ctx context.Context, collectorAddr string) (func(), error) {
	if collectorAddr == "" {
		slog.Warn("未配置 OTEL Collector，追踪功能已禁用")
		return func() {}, nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(collectorAddr),
	)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String("ai-gateway-core"),
		),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	return func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			slog.Error("关闭 TracerProvider 失败", "error", err)
		}
	}, nil
}

var (
	// RequestIDHeader 是用于透传的追踪 ID 请求头 Key。
	RequestIDHeader = "x-request-id"

	// RequestsTotal 统计已处理的请求总数。
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "处理的请求总数",
	}, []string{"status", "model"})

	// TokenUsage 统计消耗的 Token 总量。
	TokenUsage = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_token_usage_total",
		Help: "消耗的 Token 总数",
	}, []string{"model"})

	// Latency 记录请求耗时分布。
	Latency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_latency_seconds",
		Help:    "请求延迟",
		Buckets: prometheus.DefBuckets,
	}, []string{"model"})

	// AuthTotal 统计认证尝试次数。
	AuthTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_auth_total",
		Help: "认证尝试总数",
	}, []string{"status", "reason"})

	// RateLimitedTotal 统计被限流的请求总数。
	RateLimitedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_rate_limited_total",
		Help: "被限流的请求总数",
	}, []string{"key_label"})

	// RouteDecisions 统计路由决策次数（按策略和目标节点）。
	RouteDecisions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_route_decisions_total",
		Help: "路由决策总数",
	}, []string{"strategy", "node"})

	// NodeLatency 记录每个节点的调用延迟分布。
	NodeLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_node_latency_seconds",
		Help:    "每个节点的调用延迟",
		Buckets: prometheus.DefBuckets,
	}, []string{"node"})
	
	// CacheHitsTotal 统计语义缓存的命中与未命中次数。
	CacheHitsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_cache_hits_total",
		Help: "语义缓存命中/未命中总数",
	}, []string{"status", "model"})

	// CircuitBreakerChanges 统计熔断器状态转换次数。
	CircuitBreakerChanges = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_circuit_breaker_changes_total",
		Help: "熔断器状态迁移次数",
	}, []string{"node", "state"})

	// ProviderErrors 统计各上游 Provider 的错误分布。
	ProviderErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_provider_errors_total",
		Help: "上游提供商错误总数",
	}, []string{"node", "error_type"})
)

// InitLogger 初始化全局 JSON 结构化日志。
func InitLogger() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
}

// NewOutContext 创建带有追踪 ID 的出口 gRPC 上下文。
func NewOutContext(ctx context.Context, requestID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, RequestIDHeader, requestID)
}
