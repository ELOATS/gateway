// Package observability handles metrics, tracing context, and structured logging.
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
	// RequestIDHeader is the header key for propagation.
	RequestIDHeader = "x-request-id"

	// RequestsTotal counts processed requests.
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "Total processed requests",
	}, []string{"status", "model"})

	// TokenUsage counts total tokens.
	TokenUsage = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_token_usage_total",
		Help: "Total tokens consumed",
	}, []string{"model"})

	// Latency records distribution.
	Latency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_latency_seconds",
		Help:    "Request latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"model"})

	// AuthTotal counts authentication attempts.
	AuthTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_auth_total",
		Help: "Total authentication attempts",
	}, []string{"status", "reason"})

	// RateLimitedTotal counts requests that were rate limited.
	RateLimitedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_rate_limited_total",
		Help: "Total rate limited requests",
	}, []string{"key_label"})
)

// InitLogger initializes global JSON logging.
func InitLogger() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
}

// NewOutContext creates outgoing gRPC context with trace ID.
func NewOutContext(ctx context.Context, requestID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, RequestIDHeader, requestID)
}
