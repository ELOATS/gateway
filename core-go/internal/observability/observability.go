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

// InitTracer 初始化 OTEL trace exporter。
// 如果没有配置 collector，则显式降级为“关闭 tracing”，而不是让启动失败。
func InitTracer(ctx context.Context, collectorAddr string) (func(), error) {
	if collectorAddr == "" {
		slog.Warn("otel collector not configured; tracing disabled")
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
			slog.Error("failed to shutdown tracer provider", "error", err)
		}
	}, nil
}

var (
	// RequestIDHeader 用于在 HTTP -> gRPC 调用链之间透传请求 ID。
	RequestIDHeader = "x-request-id"

	// RequestsTotal 统计请求最终落地到的 HTTP 结果标签。
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "Total number of processed requests",
	}, []string{"status", "model"})

	TokenUsage = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_token_usage_total",
		Help: "Total number of consumed tokens",
	}, []string{"model"})

	Latency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_latency_seconds",
		Help:    "Request latency distribution",
		Buckets: prometheus.DefBuckets,
	}, []string{"model"})

	AuthTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_auth_total",
		Help: "Authentication attempts",
	}, []string{"status", "reason"})

	RateLimitedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_rate_limited_total",
		Help: "Requests rejected by rate limiting",
	}, []string{"key_label"})

	RouteDecisions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_route_decisions_total",
		Help: "Routing decisions grouped by strategy and node",
	}, []string{"strategy", "node"})

	NodeLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_node_latency_seconds",
		Help:    "Upstream node latency distribution",
		Buckets: prometheus.DefBuckets,
	}, []string{"node"})

	CacheHitsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_cache_hits_total",
		Help: "Semantic cache hits and misses",
	}, []string{"status", "model"})

	TTFT = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_ttft_seconds",
		Help:    "Time to first token for streaming responses",
		Buckets: []float64{0.1, 0.2, 0.5, 1.0, 2.0, 5.0, 10.0},
	}, []string{"model", "node"})

	TPS = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_tps",
		Help:    "Streaming token generation throughput",
		Buckets: prometheus.LinearBuckets(10, 10, 10),
	}, []string{"model", "node"})

	CircuitBreakerChanges = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_circuit_breaker_changes_total",
		Help: "Circuit breaker state transitions",
	}, []string{"node", "state"})

	ProviderErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_provider_errors_total",
		Help: "Provider errors grouped by node and type",
	}, []string{"node", "error_type"})

	AuditDroppedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "gateway_audit_dropped_total",
		Help: "Dropped audit log records",
	})

	// DegradedEventsTotal 用于观测 fail-open-with-audit 或其他显式降级事件的频率。
	DegradedEventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_degraded_events_total",
		Help: "Degraded or fail-open-with-audit events emitted by the gateway",
	}, []string{"component", "reason"})

	// DependencyHealth / DependencyRequired / GatewayReadiness 共同支撑 readiness 可视化与告警。
	DependencyHealth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_dependency_health",
		Help: "Runtime dependency health state (1 healthy, 0 unhealthy)",
	}, []string{"dependency", "status", "failure_mode", "version"})

	DependencyRequired = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_dependency_required",
		Help: "Whether a runtime dependency is required for readiness (1 required, 0 optional)",
	}, []string{"dependency"})

	GatewayReadiness = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "gateway_readiness",
		Help: "Gateway readiness based on required dependency health (1 ready, 0 not ready)",
	})
)

// InitLogger 使用统一的 JSON logger，方便后续采集与检索。
func InitLogger() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
}

// NewOutContext 将请求 ID 注入到下游 gRPC metadata 中。
func NewOutContext(ctx context.Context, requestID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, RequestIDHeader, requestID)
}

// RecordDependencyStatus 把单个依赖的最新状态写入 Prometheus。
func RecordDependencyStatus(name, status, failureMode, version string, required, healthy bool) {
	DependencyRequired.WithLabelValues(name).Set(boolToFloat(required))
	DependencyHealth.WithLabelValues(name, status, failureMode, version).Set(boolToFloat(healthy))
}

func RemoveDependencyStatus(name, status, failureMode, version string) {
	DependencyHealth.DeleteLabelValues(name, status, failureMode, version)
}

// RecordGatewayReadiness 把当前网关 readiness 映射为 gauge 值。
func RecordGatewayReadiness(ready bool) {
	GatewayReadiness.Set(boolToFloat(ready))
}

// RecordDegradedEvent 记录显式降级事件，便于后续做告警和趋势观察。
func RecordDegradedEvent(component, reason string) {
	if reason == "" {
		reason = "unspecified"
	}
	DegradedEventsTotal.WithLabelValues(component, reason).Inc()
}

func boolToFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
