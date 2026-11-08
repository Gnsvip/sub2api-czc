package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"strings"
)

const googleAPIKeyAuthContextKey = "_google_api_key_auth"

// APIKeyAuthGoogle uses the same admission checks as other gateway protocols.
func APIKeyAuthGoogle(keys *service.APIKeyService, cfg *config.Config) gin.HandlerFunc {
	return APIKeyAuthWithSubscriptionGoogle(keys, nil, cfg)
}

// APIKeyAuthWithSubscriptionGoogle preserves CZC authentication, IP ACL,
// exclusive-group authorization and billing checks, with Google's credential
// precedence and error envelope for native Gemini clients.
func APIKeyAuthWithSubscriptionGoogle(keys *service.APIKeyService, subscriptions *service.SubscriptionService, cfg *config.Config) gin.HandlerFunc {
	auth := apiKeyAuthWithSubscription(keys, subscriptions, cfg)
	return func(c *gin.Context) {
		c.Set(googleAPIKeyAuthContextKey, true)
		auth(c)
	}
}

// extractAPIKeyForGoogle extracts API key for Google/Gemini endpoints.
// Priority: x-goog-api-key > Authorization: Bearer > x-api-key > query key
// This allows OpenClaw and other clients using Bearer auth to work with Gemini endpoints.
func extractAPIKeyForGoogle(c *gin.Context) string {
	// 1) preferred: Gemini native header
	if k := strings.TrimSpace(c.GetHeader("x-goog-api-key")); k != "" {
		return k
	}

	// 2) fallback: Authorization: Bearer <key>
	auth := strings.TrimSpace(c.GetHeader("Authorization"))
	if auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if k := strings.TrimSpace(parts[1]); k != "" {
				return k
			}
		}
	}

	// 3) x-api-key header (backward compatibility)
	if k := strings.TrimSpace(c.GetHeader("x-api-key")); k != "" {
		return k
	}

	// 4) query parameter key (for specific paths)
	if allowGoogleQueryKey(c.Request.URL.Path) {
		if v := strings.TrimSpace(c.Query("key")); v != "" {
			return v
		}
	}

	return ""
}

func allowGoogleQueryKey(path string) bool {
	return strings.HasPrefix(path, "/v1beta") || strings.HasPrefix(path, "/antigravity/v1beta")
}
