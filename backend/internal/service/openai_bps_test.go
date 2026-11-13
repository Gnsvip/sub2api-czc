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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func bpsTestProxies() []Proxy {
	return []Proxy{{ID: 1, Status: StatusActive, Protocol: "http", Host: "exit-a.test", Port: 8080}, {ID: 2, Status: StatusActive, Protocol: "http", Host: "exit-b.test", Port: 8080}}
}

func TestBPSSessionsStickyConcurrentAndIsolated(t *testing.T) {
	var pool bpsSessionPool
	now := time.Now()
	first, release, err := pool.acquire("account:1/key:1/thread:a", bpsTestProxies(), now)
	require.NoError(t, err)
	defer release(false)
	var wg sync.WaitGroup
	var wrong atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, done, err := pool.acquire("account:1/key:1/thread:a", bpsTestProxies(), now)
			if err != nil {
				wrong.Add(1)
				return
			}
			defer done(false)
			if p.ID != first.ID {
				wrong.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Zero(t, wrong.Load())
	other, done, err := pool.acquire("account:1/key:1/thread:b", bpsTestProxies(), now)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, other.ID)
	done(false)
	done(false) // idempotent release
	other2, done2, err := pool.acquire("account:1/key:1/thread:b", bpsTestProxies(), now)
	require.NoError(t, err)
	require.Equal(t, other.ID, other2.ID)
	done2(false)
}

func TestBPSSessionsFailureDoesNotMoveLiveSession(t *testing.T) {
	var pool bpsSessionPool
	now := time.Now()
	_, a, err := pool.acquire("one", bpsTestProxies(), now)
	require.NoError(t, err)
	_, b, err := pool.acquire("one", bpsTestProxies(), now)
	require.NoError(t, err)
	a(true)
	_, _, err = pool.acquire("one", bpsTestProxies(), now)
	require.ErrorIs(t, err, errBPSProxyUnavailable)
	b(false)
	p, done, err := pool.acquire("one", bpsTestProxies(), now)
	require.NoError(t, err)
	require.EqualValues(t, 2, p.ID)
	done(false)
}

func TestBPSSessionsEmptyExpiredRemovedAndBounded(t *testing.T) {
	var pool bpsSessionPool
	now := time.Now()
	_, _, err := pool.acquire("empty", nil, now)
	require.ErrorIs(t, err, errBPSProxyUnavailable)
	expired := now.Add(-time.Second)
	proxies := bpsTestProxies()[:1]
	proxies[0].ExpiresAt = &expired
	_, _, err = pool.acquire("expired", proxies, now)
	require.ErrorIs(t, err, errBPSProxyUnavailable)
	proxies[0].ExpiresAt = nil
	_, done, err := pool.acquire("live", proxies, now)
	require.NoError(t, err)
	_, _, err = pool.acquire("live", bpsTestProxies()[1:], now)
	require.ErrorIs(t, err, errBPSProxyUnavailable)
	done(false)
	for i := 0; i < bpsSessionLimit+10; i++ {
		_, release, err := pool.acquire(fmt.Sprint(i), proxies, now)
		require.NoError(t, err)
		release(false)
	}
	require.LessOrEqual(t, len(pool.bindings), bpsSessionLimit)
	_, release, err := pool.acquire("after-expiry", proxies, time.Now().Add(bpsSessionIdleTTL+time.Second))
	require.NoError(t, err)
	require.Len(t, pool.bindings, 1)
	release(false)
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
	require.Error(t, validateOpenAIBPSSettings(a))
	a.Extra = nil
	require.True(t, a.IsSchedulable())
	require.True(t, a.UsesOpenAICodexProtocol())
	a.Extra = map[string]any{OpenAIExcelBPSEnabledKey: "true"}
	require.Error(t, validateOpenAIBPSSettings(a))
}

func TestBPSBulkSettingsRequireExcelCredentials(t *testing.T) {
	a := bpsTestAccount()
	input := &BulkUpdateAccountsInput{AccountIDs: []int64{a.ID}, Extra: map[string]any{OpenAIExcelBPSSessionProxyKey: true}}
	settings, err := normalizeBulkOpenAISettings(input)
	require.NoError(t, err)
	require.True(t, settings.any())
	require.NoError(t, validateBulkOpenAISettingsTargets(input, settings, map[int64]*Account{a.ID: a}))
	a.Credentials["client_id"] = openai.ClientID
	require.Error(t, validateBulkOpenAISettingsTargets(input, settings, map[int64]*Account{a.ID: a}))
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

func TestBPSForwardNoReplayAndNoDirectFallback(t *testing.T) {
	for _, failure := range []string{"empty_pool", "transport", "403", "503", "broken_stream"} {
		t.Run(failure, func(t *testing.T) {
			body := []byte(`{"model":"gpt-6.1-sol","input":"hello","stream":true}`)
			c, _ := bpsTestContext(string(body))
			a := bpsTestAccount()
			a.Extra[OpenAIExcelBPSSessionProxyKey] = true
			calls := 0
			svc := &OpenAIGatewayService{cfg: &config.Config{}, bpsProxyRepo: bpsProxySourceStub{proxies: bpsTestProxies()}}
			if failure == "empty_pool" {
				svc.bpsProxyRepo = bpsProxySourceStub{}
			}
			svc.httpUpstream = bpsHTTPStub{do: func(r *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
				calls++
				require.Equal(t, int64(71), id)
				require.Equal(t, 3, concurrency)
				require.NotEmpty(t, proxy)
				require.Equal(t, "bps.openai.com", r.URL.Host)
				require.Empty(t, r.Header.Get("Cookie"))
				require.Equal(t, HTTPUpstreamProfileBPS, HTTPUpstreamProfileFromContext(r.Context()))
				if failure == "transport" {
					return nil, errors.New("connection lost")
				}
				status := 403
				if failure == "503" {
					status = 503
				}
				if failure == "broken_stream" {
					status = 200
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
			}}
			_, err := svc.forwardBPS(context.Background(), c, a, body, time.Now())
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover))
			require.True(t, IsResponseCommitted(c))
			if failure == "empty_pool" {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
			for _, b := range svc.bpsSessions.bindings {
				require.Zero(t, b.active)
			}
		})
	}
}

func TestBPSForwardRouteAndDisabledBoundary(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			a := bpsTestAccount()
			a.Extra[OpenAIExcelBPSEnabledKey] = enabled
			body := []byte(`{"model":"gpt-6.1-sol","input":"hello"}`)
			c, _ := bpsTestContext(string(body))
			up := &httpUpstreamRecorder{err: errors.New("stop after capturing route")}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: up}
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
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: up}
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
	for _, input := range []string{`[{"type":"reasoning","encrypted_content":"ciphertext"}]`, `[{"type":"compaction_trigger"}]`} {
		body := []byte(`{"model":"gpt-6.1-sol","input":` + input + `}`)
		c, rec := bpsTestContext(string(body))
		svc := &OpenAIGatewayService{}
		_, err := svc.forwardBPS(context.Background(), c, bpsTestAccount(), body, time.Now())
		require.Error(t, err)
		require.Equal(t, 400, rec.Code)
		require.NotContains(t, rec.Body.String(), "ciphertext")
	}
}

func TestBPSAdminConnectionTestUsesBPSRoute(t *testing.T) {
	body := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"
	up := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}}
	gateway := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: up}
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
