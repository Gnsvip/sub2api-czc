package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func bpsTestProxies() []Proxy {
	return []Proxy{{ID: 1, Status: StatusActive, Protocol: "http", Host: "exit-a.test", Port: 8080}, {ID: 2, Status: StatusActive, Protocol: "http", Host: "exit-b.test", Port: 8080}}
}

func bpsTestAccount() *Account {
	return &Account{ID: 71, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 3,
		Credentials: map[string]any{"client_id": openai.ExcelClientID, "access_token": "test-only-token", "chatgpt_account_id": "test-account"},
		Extra:       map[string]any{OpenAIExcelBPSEnabledKey: true}}
}

func bpsTestContext(body string) (*gin.Context, *httptest.ResponseRecorder) {
	r := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(r)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	return c, r
}

func TestBPSRequestScope(t *testing.T) {
	c, _ := bpsTestContext(`{}`)
	c.Request.Header.Set("session-id", "stable")
	a := bpsRequestScope(c, 1, []byte(`{"input":"first"}`))
	require.Equal(t, a, bpsRequestScope(c, 1, []byte(`{"input":"second"}`)))
	require.NotEqual(t, a, bpsRequestScope(c, 2, []byte(`{}`)))
	c.Set("api_key", &APIKey{ID: 37})
	require.NotEqual(t, a, bpsRequestScope(c, 1, []byte(`{}`)))
	x, _ := bpsTestContext(`{}`)
	y, _ := bpsTestContext(`{}`)
	require.NotEqual(t, bpsRequestScope(x, 1, nil), bpsRequestScope(y, 1, nil))
}

func TestBPSDefaultOffAndSettingsValidation(t *testing.T) {
	a := bpsTestAccount()
	a.Extra = nil
	require.False(t, a.isBPSEnabled())
	require.False(t, a.IsSchedulable())
	a.Extra = map[string]any{OpenAIExcelBPSEnabledKey: true, OpenAIExcelBPSSessionProxyKey: true, OpenAIExcelBPSModelsKey: []any{"gpt-6.1-sol"}}
	require.NoError(t, validateOpenAIBPSSettings(a))
	require.True(t, a.bpsModelAllowed("gpt-6.1-sol"))
	require.False(t, a.bpsModelAllowed("other"))
	a.Credentials["client_id"] = openai.ClientID
	require.NoError(t, validateOpenAIBPSSettings(a))
	a.Extra = nil
	require.True(t, a.IsSchedulable())
	require.True(t, a.UsesOpenAICodexProtocol())
	a.Extra = map[string]any{OpenAIExcelBPSEnabledKey: "true"}
	require.Error(t, validateOpenAIBPSSettings(a))
}

func TestBPSBulkSettingsAllowChatGPTOAuth(t *testing.T) {
	a := bpsTestAccount()
	input := &BulkUpdateAccountsInput{AccountIDs: []int64{a.ID}, Extra: map[string]any{OpenAIExcelBPSSessionProxyKey: true}}
	settings, err := normalizeBulkOpenAISettings(input)
	require.NoError(t, err)
	require.True(t, settings.any())
	require.NoError(t, validateBulkOpenAISettingsTargets(input, settings, map[int64]*Account{a.ID: a}))
	a.Credentials["client_id"] = openai.ClientID
	require.NoError(t, validateBulkOpenAISettingsTargets(input, settings, map[int64]*Account{a.ID: a}))
}

type bpsProxySourceStub struct {
	proxies []Proxy
	err     error
}

func (s bpsProxySourceStub) ListActive(context.Context) ([]Proxy, error) { return s.proxies, s.err }

type bpsHTTPStub struct {
	HTTPUpstream
	do func(*http.Request, string, int64, int) (*http.Response, error)
}

func (s bpsHTTPStub) Do(r *http.Request, p string, a int64, n int) (*http.Response, error) {
	return s.do(r, p, a, n)
}

func TestBPSForwardRouteAndDisabledBoundary(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			a := bpsTestAccount()
			a.Extra[OpenAIExcelBPSEnabledKey] = enabled
			body := []byte(`{"model":"gpt-6.1-sol","input":"hello"}`)
			c, _ := bpsTestContext(string(body))
			up := &httpUpstreamRecorder{err: errors.New("stop after capturing route")}
			svc := excelBPSTestService(up)
			_, err := svc.Forward(context.Background(), c, a, body)
			require.Error(t, err)
			if enabled {
				require.Len(t, up.requests, 1)
				require.Equal(t, "bps.openai.com", up.lastReq.URL.Host)
			} else {
				require.Empty(t, up.requests)
			}
		})
	}
}

func TestBPSForwardResponsesAndUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"gpt-6.1-sol","input":"hello","stream":%t}`, stream))
			c, rec := bpsTestContext(string(body))
			raw := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bps\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":12,\"output_tokens\":3}}}\n\n"
			up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(raw))}}
			svc := excelBPSTestService(up)
			result, err := svc.forwardBPS(context.Background(), c, bpsTestAccount(), body, time.Now())
			require.NoError(t, err)
			require.Equal(t, "resp_bps", result.ResponseID)
			require.Equal(t, "/basispoints/api/responses", result.UpstreamEndpoint)
			require.EqualValues(t, 12, result.Usage.InputTokens)
			require.EqualValues(t, 3, result.Usage.OutputTokens)
			require.Contains(t, rec.Body.String(), "hello")
			require.Equal(t, 200, rec.Code)
			if stream {
				require.NotNil(t, result.FirstTokenMs)
			}
		})
	}
}

func TestBPSRejectUnsupportedBeforeSending(t *testing.T) {
	for _, input := range []string{`[{"role":"user","content":[{"type":"encrypted_content","encrypted_content":"ciphertext"}]}]`, `[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"ciphertext","format":"wav"}}]}]`} {
		body := []byte(`{"model":"gpt-6.1-sol","input":` + input + `}`)
		c, rec := bpsTestContext(string(body))
		upstream := &httpUpstreamRecorder{}
		svc := excelBPSTestService(upstream)
		_, err := svc.forwardBPS(context.Background(), c, bpsTestAccount(), body, time.Now())
		require.Error(t, err)
		require.Equal(t, 400, rec.Code)
		require.NotContains(t, rec.Body.String(), "ciphertext")
		require.Empty(t, upstream.requests)
	}
}

func TestBPSAdminConnectionTestUsesBPSRoute(t *testing.T) {
	body := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"
	up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}}
	gateway := excelBPSTestService(up)
	svc := &AccountTestService{openaiGatewayService: gateway}
	c, rec := bpsTestContext(`{}`)
	require.NoError(t, svc.testOpenAIAccountConnection(c, bpsTestAccount(), "gpt-6.1-sol", "Reply OK", AccountTestModeDefault))
	require.Equal(t, "bps.openai.com", up.lastReq.URL.Host)
	require.Contains(t, rec.Body.String(), `"text":"OK"`)
	require.Contains(t, rec.Body.String(), `"success":true`)
	require.Len(t, up.requests, 1)
}

func TestBPSOAuthSeparateAuthorization(t *testing.T) {
	svc := NewOpenAIOAuthService(nil, &openaiOAuthClientAuthURLStub{})
	defer svc.Stop()
	result, err := svc.GenerateAuthURL(context.Background(), nil, "https://invalid.test/callback", openai.OAuthPlatformExcel)
	require.NoError(t, err)
	u, err := url.Parse(result.AuthURL)
	require.NoError(t, err)
	require.Equal(t, openai.ExcelAuthorizeURL, u.Scheme+"://"+u.Host+u.Path)
	require.Equal(t, openai.ExcelClientID, u.Query().Get("client_id"))
	require.Equal(t, openai.ExcelRedirectURI, u.Query().Get("redirect_uri"))
	require.Equal(t, "S256", u.Query().Get("code_challenge_method"))
	require.Empty(t, u.Query().Get("codex_cli_simplified_flow"))
	session, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok)
	require.Equal(t, openai.ExcelClientID, session.ClientID)
	require.Equal(t, openai.ExcelRedirectURI, session.RedirectURI)
	require.NotEmpty(t, session.CodeVerifier)
	require.False(t, bytes.Contains([]byte(result.AuthURL), []byte(session.CodeVerifier)))
}

func TestBPSOAuthExchangeUsesSessionClientAndCallback(t *testing.T) {
	client := &openaiOAuthClientStateStub{}
	svc := NewOpenAIOAuthService(nil, client)
	defer svc.Stop()
	result, err := svc.GenerateAuthURL(context.Background(), nil, "", openai.OAuthPlatformExcel)
	require.NoError(t, err)
	session, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok)
	info, err := svc.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{SessionID: result.SessionID, Code: "test-code", State: session.State, RedirectURI: "https://wrong.example/callback"})
	require.NoError(t, err)
	require.Equal(t, openai.ExcelClientID, client.lastClientID)
	require.Equal(t, openai.ExcelRedirectURI, client.lastRedirectURI)
	require.Equal(t, openai.ExcelClientID, info.ClientID)
	_, ok = svc.sessionStore.Get(result.SessionID)
	require.False(t, ok)
}
