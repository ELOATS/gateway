package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-gateway/core/internal/runtime"
	"github.com/gin-gonic/gin"
)

func TestAdminHandlerListDependencies(t *testing.T) {
	gin.SetMode(gin.TestMode)

	status := runtime.NewSystemStatus()
	status.Set(runtime.DependencyStatus{
		Name:        "nitro",
		Required:    true,
		Healthy:     true,
		Status:      "ready",
		Version:     "wasm",
		FailureMode: "fail_closed",
	})
	status.Set(runtime.DependencyStatus{
		Name:        "python",
		Required:    false,
		Healthy:     false,
		Status:      "degraded",
		Reason:      "optional path unavailable",
		FailureMode: "fail_open_with_audit",
	})

	handler := NewAdminHandler(nil, nil, status)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest(http.MethodGet, "/admin/dependencies", nil)
	c.Request = req

	handler.ListDependencies(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, needle := range []string{"nitro", "python", "fail_closed", "fail_open_with_audit"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("expected body to contain %q, got %s", needle, body)
		}
	}
}
