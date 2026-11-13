//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExcelBPSModelSelection(t *testing.T) {
	a := bpsUpstreamTestAccount()
	require.True(t, a.IsExcelBPSEnabledForModel("gpt-6-sol"), "legacy all-model setting")
	a.Extra["openai_excel_bps_models"] = []any{"gpt-6-astra"}
	require.True(t, a.IsExcelBPSEnabledForModel("gpt-6-astra"))
	require.False(t, a.IsExcelBPSEnabledForModel("gpt-6-sol"))
	require.False(t, a.IsExcelBPSEnabledForModel("gpt-6-astra-other"))
	a.Credentials["model_mapping"] = map[string]any{"alias": "gpt-6-astra", "gpt-6-astra": "gpt-6-sol"}
	require.True(t, a.IsExcelBPSEnabledForModel("alias"))
	require.False(t, a.IsExcelBPSEnabledForModel("gpt-6-astra"), "selection matches mapped upstream")
	require.True(t, a.isExcelBPSUpstreamModelEnabled("gpt-6-astra"), "already mapped names must not map again")
	for _, models := range []any{[]any{}, []string{}, nil, "gpt-6-astra", []any{42, false}} {
		a.Extra["openai_excel_bps_models"] = models
		require.False(t, a.IsExcelBPSEnabledForModel("alias"))
		require.False(t, a.isExcelBPSAllModelsEnabled())
	}
	a.Extra["openai_excel_bps_models"] = []string{" gpt-6-astra "}
	require.True(t, a.IsExcelBPSEnabledForModel("alias"))
	a.Extra["openai_excel_bps"] = false
	require.False(t, a.IsExcelBPSEnabledForModel("alias"))
	var absent *Account
	require.False(t, absent.IsExcelBPSEnabledForModel("gpt-6-astra"))
}

func TestExcelBPSSelectedModelForwarding(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-sol"} {
		t.Run(model, func(t *testing.T) {
			wire := fmt.Sprintf("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"model\":%q,\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", model)
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
			svc := excelBPSTestService(upstream)
			a := bpsUpstreamTestAccount()
			a.Extra["openai_excel_bps_models"] = []string{"gpt-6-astra"}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			result, err := svc.Forward(context.Background(), c, a, []byte(fmt.Sprintf(`{"model":%q,"stream":true,"input":"test"}`, model)))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, upstream.lastReq)
			if model == "gpt-6-astra" {
				require.Equal(t, "bps.openai.com", upstream.lastReq.URL.Host)
				require.Equal(t, "/basispoints/api/responses", upstream.lastReq.URL.Path)
			} else {
				require.Equal(t, "chatgpt.com", upstream.lastReq.URL.Host)
				require.Equal(t, "/backend-api/codex/responses", upstream.lastReq.URL.Path)
			}
		})
	}
}

func TestExcelBPSSelectedModelsPreserveNativeTransport(t *testing.T) {
	a := bpsUpstreamTestAccount()
	a.Extra["openai_excel_bps_models"] = []string{"gpt-6-astra"}
	a.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
	a.Extra["openai_oauth_responses_websockets_v2_enabled"] = true
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := &OpenAIGatewayService{cfg: cfg}
	require.False(t, a.IsOpenAIWSForceHTTPEnabled())
	require.True(t, a.IsOpenAIResponsesWebSocketV2Enabled())
	require.Equal(t, OpenAIWSIngressModeCtxPool, a.ResolveOpenAIResponsesWebSocketV2Mode("off"))
	for _, transport := range []OpenAIUpstreamTransport{OpenAIUpstreamTransportResponsesWebsocketV2, OpenAIUpstreamTransportResponsesWebsocketV2Ingress} {
		require.False(t, svc.isOpenAIAccountTransportCompatible(a, transport, "gpt-6-astra"))
		require.True(t, svc.isOpenAIAccountTransportCompatible(a, transport, "gpt-6-sol"))
	}
	require.True(t, svc.isOpenAIAccountTransportCompatible(a, OpenAIUpstreamTransportHTTPSSE, "gpt-6-astra"))
}

func TestExcelBPSSelectedCompactKeepsExcelModel(t *testing.T) {
	a := bpsUpstreamTestAccount()
	a.Extra["openai_excel_bps_models"] = []string{"gpt-6-astra"}
	a.Credentials["model_mapping"] = map[string]any{"alias": "gpt-6-astra"}
	a.Credentials["compact_model_mapping"] = map[string]any{"alias": "gpt-6-sol"}
	require.Equal(t, "gpt-6-astra", resolveOpenAIAccountUpstreamModelForRequest(a, "alias", true))
}

func TestExcelBPSAliasesLeaveNativeOAuthUnchanged(t *testing.T) {
	account := bpsUpstreamTestAccount()
	account.Extra["openai_excel_bps_models"] = []string{"bps-target"}
	account.Credentials["model_mapping"] = map[string]any{"bps-alias": "bps-target", "native-alias": "native-target"}
	require.Equal(t, "bps-alias", account.GetMappedModel("bps-alias"))
	require.Equal(t, "bps-target", account.getExcelBPSMappedModel("bps-alias"))
	require.Equal(t, "native-alias", account.GetMappedModel("native-alias"))
	require.Nil(t, account.GetModelMapping())
	account.Extra["openai_excel_bps"] = false
	require.Equal(t, "bps-alias", account.GetMappedModel("bps-alias"))
}

func TestExcelBPSGlobalDisablePreservesNativeRequest(t *testing.T) {
	account := bpsUpstreamTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"gpt-6.1-sol": "gpt-6-astra"}
	account.Extra["openai_excel_bps_models"] = []string{"gpt-6-astra"}
	wire := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_native\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}}
	svc := excelBPSTestService(upstream)
	svc.settingService = &SettingService{settingRepo: &settingRepoStub{}}
	svc.excelBPSCooldownUntil.Store(account.ID, time.Now().Add(time.Minute))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-6.1-sol","stream":true,"input":"test"}`))
	require.NoError(t, err)
	require.Equal(t, "chatgpt.com", upstream.lastReq.URL.Host)
	require.Contains(t, string(upstream.lastBody), `"model":"gpt-6.1-sol"`)
	require.True(t, account.IsExcelBPSEnabled(), "the shared account is not mutated")
	view := svc.accountForExcelBPSRouting(context.Background(), account)
	require.False(t, svc.isExcelBPSCoolingDown(view, "gpt-6.1-sol"))
	require.Equal(t, "gpt-6.1-sol", resolveOpenAIAccountUpstreamModelForRequest(view, "gpt-6.1-sol", false))
}
