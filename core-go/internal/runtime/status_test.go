package runtime

import (
	"testing"

	"github.com/ai-gateway/core/internal/observability"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSystemStatusUpdatesMetrics(t *testing.T) {
	status := NewSystemStatus()

	status.Set(DependencyStatus{
		Name:        "nitro",
		Required:    true,
		Healthy:     false,
		Status:      "degraded",
		Version:     "wasm",
		FailureMode: "fail_closed",
	})

	if got := testutil.ToFloat64(observability.GatewayReadiness); got != 0 {
		t.Fatalf("expected readiness gauge to be 0, got %v", got)
	}
	if got := testutil.ToFloat64(observability.DependencyRequired.WithLabelValues("nitro")); got != 1 {
		t.Fatalf("expected nitro required gauge to be 1, got %v", got)
	}
	if got := testutil.ToFloat64(observability.DependencyHealth.WithLabelValues("nitro", "degraded", "fail_closed", "wasm")); got != 0 {
		t.Fatalf("expected nitro health gauge to be 0, got %v", got)
	}

	status.Set(DependencyStatus{
		Name:        "nitro",
		Required:    true,
		Healthy:     true,
		Status:      "ready",
		Version:     "grpc:localhost:50052",
		FailureMode: "fail_closed",
	})

	if got := testutil.ToFloat64(observability.GatewayReadiness); got != 1 {
		t.Fatalf("expected readiness gauge to be 1, got %v", got)
	}
	if got := testutil.ToFloat64(observability.DependencyHealth.WithLabelValues("nitro", "ready", "fail_closed", "grpc:localhost:50052")); got != 1 {
		t.Fatalf("expected nitro ready gauge to be 1, got %v", got)
	}
}
