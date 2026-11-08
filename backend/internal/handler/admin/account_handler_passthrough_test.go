package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type astraProbeAdminStub struct {
	service.AdminService
	calls int
}

func (s *astraProbeAdminStub) SetOpenAIAstraProbeEnabled(_ context.Context, id int64, enabled bool) (*service.Account, error) {
	s.calls++
	return &service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Extra: map[string]any{service.OpenAIAstraProbeEnabledExtraKey: enabled}}, nil
}

func TestAccountHandler_AstraProbeSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, id, body string
		status         int
	}{
		{"disable", "12", `{"enabled":false}`, http.StatusOK},
		{"enable", "12", `{"enabled":true}`, http.StatusOK},
		{"missing", "12", `{}`, http.StatusBadRequest},
		{"wrong type", "12", `{"enabled":"false"}`, http.StatusBadRequest},
		{"invalid id", "invalid", `{"enabled":false}`, http.StatusBadRequest},
		{"zero id", "0", `{"enabled":false}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &astraProbeAdminStub{}
			h := &AccountHandler{adminService: svc}
			router := gin.New()
			router.PUT("/accounts/:id/astra-probe", h.SetOpenAIAstraProbeEnabled)
			req := httptest.NewRequest(http.MethodPut, "/accounts/"+tc.id+"/astra-probe", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status == http.StatusOK {
				require.Equal(t, 1, svc.calls)
				var payload struct {
					Data struct {
						Extra map[string]any `json:"extra"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
				require.Equal(t, tc.name == "enable", payload.Data.Extra[service.OpenAIAstraProbeEnabledExtraKey])
			} else {
				require.Zero(t, svc.calls)
			}
		})
	}
}

func TestAccountHandler_Create_AnthropicAPIKeyPassthroughExtraForwarded(t *testing.T) {
	gin.SetMode(gin.TestMode)

	adminSvc := newStubAdminService()
	handler := NewAccountHandler(
		adminSvc,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	router := gin.New()
	router.POST("/api/v1/admin/accounts", handler.Create)

	body := map[string]any{
		"name":     "anthropic-key-1",
		"platform": "anthropic",
		"type":     "apikey",
		"credentials": map[string]any{
			"api_key":  "sk-ant-xxx",
			"base_url": "https://api.anthropic.com",
		},
		"extra": map[string]any{
			"anthropic_passthrough": true,
		},
		"concurrency": 1,
		"priority":    1,
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, adminSvc.createdAccounts, 1)

	created := adminSvc.createdAccounts[0]
	require.Equal(t, "anthropic", created.Platform)
	require.Equal(t, "apikey", created.Type)
	require.NotNil(t, created.Extra)
	require.Equal(t, true, created.Extra["anthropic_passthrough"])
}
