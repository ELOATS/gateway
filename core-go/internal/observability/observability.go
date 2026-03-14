// Package observability 提供监控指标、请求追踪上下文和结构化日志处理。
package observability

import (
	"context"
	"log/slog"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc/metadata"
)

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
