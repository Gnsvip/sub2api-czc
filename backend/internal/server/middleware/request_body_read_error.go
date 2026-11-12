package middleware

import "github.com/gin-gonic/gin"

// AbortRequestBodyReadFailure preserves the shared ingress error envelope.
func AbortRequestBodyReadFailure(c *gin.Context, err error) {
 if c == nil { return }
 if RejectRequestBodyReadError(c, "middleware.request_body", err) { return }
 AbortWithRequestBodyReadFailure(c, "middleware.request_body", err)
}
