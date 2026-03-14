package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	// RequestIDKey is the key used to store the Request-ID in the gin.Context.
	RequestIDKey = "request_id"
	// HeaderXRequestID is the standard header for request tracking.
	HeaderXRequestID = "X-Request-ID"
)

// RequestID middleware ensures every request has a unique ID.
// It checks for an existing X-Request-ID header, otherwise generates a new UUID.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		rid := c.GetHeader(HeaderXRequestID)
		if rid == "" {
			rid = uuid.New().String()
		}

		// Inject into context for handlers to use
		c.Set(RequestIDKey, rid)

		// Set response header
		c.Header(HeaderXRequestID, rid)

		c.Next()
	}
}
