package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-gateway/core/internal/runtime"
	"github.com/gin-gonic/gin"
)

func TestReadyzReturnsServiceUnavailableWhenRequiredDependencyIsDown(t *testing.T) {
	gin.SetMode(gin.TestMode)

	status := runtime.NewSystemStatus()
	status.Set(runtime.DependencyStatus{
		Name:        "nitro",
		Required:    true,
		Healthy:     false,
		Status:      "degraded",
		Reason:      "startup check failed",
		Version:     "wasm",
		FailureMode: "fail_closed",
	})

	r := gin.New()
	installStatusRoutes(r, status)

	req, _ := http.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestReadyzReturnsDependencySnapshotWhenReady(t *testing.T) {
	gin.SetMode(gin.TestMode)

	status := runtime.NewSystemStatus()
	status.Set(runtime.DependencyStatus{
		Name:        "nitro",
		Required:    true,
		Healthy:     true,
		Status:      "ready",
		Version:     "grpc:localhost:50052",
		FailureMode: "fail_closed",
	})
	status.Set(runtime.DependencyStatus{
		Name:        "python",
		Required:    false,
		Healthy:     false,
		Status:      "degraded",
		Reason:      "optional dependency unavailable",
		FailureMode: "fail_open_with_audit",
	})

	r := gin.New()
	installStatusRoutes(r, status)

	req, _ := http.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); body == "" || !containsAll(body, "nitro", "python", "fail_closed", "fail_open_with_audit") {
		t.Fatalf("expected dependency snapshot in body, got %s", body)
	}
}

func containsAll(body string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(body, part) {
			return false
		}
	}
	return true
}
