//go:build unit

package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExcelLoginWorkerAuthenticationCannotUseAdminOrQueryTokens(t *testing.T) {
	token := strings.Repeat("synthetic", 8)
	t.Setenv("OPENAI_REAUTH_WORKER_TOKEN", token)
	svc := service.ProvideOpenAIOAuthService(nil, nil, nil, nil, nil, nil, nil, nil)
	h := &OpenAIOAuthHandler{openaiOAuthService: svc}
	for _, kind := range []string{"absent", "query", "admin", "wrong", "worker"} {
		t.Run(kind, func(t *testing.T) {
			r := gin.New()
			r.POST("/claim", h.ExcelLoginWorkerAuth, func(c *gin.Context) { c.Status(http.StatusNoContent) })
			url := "/claim"
			if kind == "query" {
				url += "?token=" + token
			}
			request := httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
			switch kind {
			case "admin":
				request.Header.Set("Authorization", "Bearer "+token)
				request.Header.Set("x-api-key", token)
			case "wrong":
				request.Header.Set("X-OpenAI-Reauth-Worker-Token", "wrong")
			case "worker":
				request.Header.Set("X-OpenAI-Reauth-Worker-Token", token)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, request)
			expected := http.StatusUnauthorized
			if kind == "worker" {
				expected = http.StatusNoContent
			}
			require.Equal(t, expected, rec.Code)
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			require.NotContains(t, rec.Body.String(), token)
		})
	}
}
