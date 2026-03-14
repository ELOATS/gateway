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
		r.Use(RateLimiter(qps, burst))
		r.GET("/test", func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
		return r
	}

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
}
