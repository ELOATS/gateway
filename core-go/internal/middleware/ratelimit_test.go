package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestRateLimiter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Low rate for testing
	qps := 10.0
	burst := 1

	setupRouter := func() *gin.Engine {
		r := gin.New()
		r.Use(RequestID())
		r.Use(RateLimiter(nil, qps, burst)) // Test local fallback
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
		return r
	}

	t.Run("Basic throttling", func(t *testing.T) {
		r := setupRouter()
		// 1. First request should pass
		req1, _ := http.NewRequest(http.MethodGet, "/test", nil)
		w1 := httptest.NewRecorder()
		r.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// 2. Immediate second request should be rate limited
		req2, _ := http.NewRequest(http.MethodGet, "/test", nil)
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
		assert.NotEmpty(t, w2.Header().Get("Retry-After"))

		// 3. After waiting, request should pass again
		time.Sleep(110 * time.Millisecond) // wait more than 1/10 second
		req3, _ := http.NewRequest(http.MethodGet, "/test", nil)
		w3 := httptest.NewRecorder()
		r.ServeHTTP(w3, req3)
		assert.Equal(t, http.StatusOK, w3.Code)
	})

	t.Run("Multi-tenant isolation", func(t *testing.T) {
		r := gin.New()
		r.Use(RequestID())

		// Helper middleware to set label
		r.Use(func(c *gin.Context) {
			if label := c.GetHeader("X-Test-Label"); label != "" {
				c.Set("key_label", label)
			}
			c.Next()
		})
		r.Use(RateLimiter(nil, qps, burst))

		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})

		// 1. Request for User A
		reqA, _ := http.NewRequest(http.MethodGet, "/test", nil)
		reqA.Header.Set("X-Test-Label", "user_a")
		wA := httptest.NewRecorder()
		r.ServeHTTP(wA, reqA)
		assert.Equal(t, http.StatusOK, wA.Code)

		// 2. Another Request for User A should fail
		reqA2, _ := http.NewRequest(http.MethodGet, "/test", nil)
		reqA2.Header.Set("X-Test-Label", "user_a")
		wA2 := httptest.NewRecorder()
		r.ServeHTTP(wA2, reqA2)
		assert.Equal(t, http.StatusTooManyRequests, wA2.Code)

		// 3. Request for User B should still pass (Isolated)
		reqB, _ := http.NewRequest(http.MethodGet, "/test", nil)
		reqB.Header.Set("X-Test-Label", "user_b")
		wB := httptest.NewRecorder()
		r.ServeHTTP(wB, reqB)
		assert.Equal(t, http.StatusOK, wB.Code)
	})
}
