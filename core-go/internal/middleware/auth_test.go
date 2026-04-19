package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ai-gateway/core/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestAuthRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)

	keys := []config.APIKeyEntry{
		{Key: "key1", Label: "admin"},
		{Key: "key2", Label: "user"},
	}

	setupRouter := func() *gin.Engine {
		r := gin.New()
		r.Use(RequestID())
		r.Use(AuthRequired(nil, keys))
		r.GET("/test", func(c *gin.Context) {
			label, _ := c.Get("key_label")
			rid, _ := c.Get(RequestIDKey)
			c.JSON(http.StatusOK, gin.H{"label": label, "request_id": rid})
		})
		return r
	}

	tests := []struct {
		name           string
		authHeader     string
		expectedStatus int
		expectedLabel  string
	}{
		{
			name:           "Valid Key 1",
			authHeader:     "Bearer key1",
			expectedStatus: http.StatusOK,
			expectedLabel:  "admin",
		},
		{
			name:           "Valid Key 2",
			authHeader:     "Bearer key2",
			expectedStatus: http.StatusOK,
			expectedLabel:  "user",
		},
		{
			name:           "Invalid Key",
			authHeader:     "Bearer wrong-key",
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "Missing Header",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "Bad Format",
			authHeader:     "key1",
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := setupRouter()
			req, _ := http.NewRequest(http.MethodGet, "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.expectedStatus == http.StatusOK {
				assert.Contains(t, w.Body.String(), tt.expectedLabel)
				assert.NotEmpty(t, w.Header().Get(HeaderXRequestID))
			}
		})
	}
}
