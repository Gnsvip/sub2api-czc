package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func zstdTestCodexAccount(t *testing.T) *Account {
	t.Helper()
	return &Account{
		ID:       42,
		Type:     AccountTypeOAuth,
		Platform: PlatformOpenAI,
		Extra:    map[string]any{},
	}
}

func codexCompressionTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := codexExactFidelityTestConfig(t)
	cfg.Gateway.OpenAIWS.CodexRequestCompressionEnabled = true
	return cfg
}

func TestMaybeCompressCodexUpstreamRequest_DefaultPlainJSON(t *testing.T) {
	for name, cfg := range map[string]*config.Config{
		"nil": nil, "zero": {}, "exact_fidelity": codexExactFidelityTestConfig(t),
	} {
		t.Run(name, func(t *testing.T) {
			plain := []byte(`{"model":"gpt-5","input":"unchanged"}`)
			req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(plain))
			require.NoError(t, err)
			originalBody := req.Body
			svc := &OpenAIGatewayService{cfg: cfg}
			svc.maybeCompressCodexUpstreamRequest(req, zstdTestCodexAccount(t))
			require.Same(t, originalBody, req.Body, "default must not consume or replace the body")
			require.Empty(t, req.Header.Get("Content-Encoding"))
			require.Equal(t, int64(len(plain)), req.ContentLength)
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, plain, body)
			replay, err := req.GetBody()
			require.NoError(t, err)
			defer replay.Close()
			body, err = io.ReadAll(replay)
			require.NoError(t, err)
			require.Equal(t, plain, body)
		})
	}
}

func TestMaybeCompressCodexUpstreamRequest_OptInWindowBound(t *testing.T) {
	plain := []byte(`{"input":"` + strings.Repeat("synthetic-payload-", 500000) + `"}`)
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(plain))
	require.NoError(t, err)
	svc := &OpenAIGatewayService{cfg: codexCompressionTestConfig(t)}
	svc.maybeCompressCodexUpstreamRequest(req, zstdTestCodexAccount(t))
	wire, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	var header zstd.Header
	require.NoError(t, header.Decode(wire))
	require.False(t, header.SingleSegment)
	require.Positive(t, header.WindowSize)
	require.LessOrEqual(t, header.WindowSize, uint64(2<<20))
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxWindow(2<<20))
	require.NoError(t, err)
	defer decoder.Close()
	decoded, err := decoder.DecodeAll(wire, nil)
	require.NoError(t, err)
	require.Equal(t, plain, decoded)
}

// Reproduce the observed HTTP boundary: valid compressed JSON receives an
// opaque 400; identical plain JSON succeeds on the first upstream attempt.
func TestCodexHTTPCompressionRejectedUpstream(t *testing.T) {
	plain := []byte(`{"input":"` + strings.Repeat("synthetic-", 160000) + `"}`)
	for _, optIn := range []bool{false, true} {
		name := "default_plain"
		if optIn {
			name = "explicit_compression"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				if req.Header.Get("Content-Encoding") == "zstd" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"detail":"Bad Request"}`)
					return
				}
				if !bytes.Equal(plain, body) {
					t.Error("request JSON changed")
					w.WriteHeader(400)
					return
				}
				_, _ = io.WriteString(w, "OK")
			}))
			defer srv.Close()
			cfg := codexExactFidelityTestConfig(t)
			cfg.Gateway.OpenAIWS.CodexRequestCompressionEnabled = optIn
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &liveWireHTTPUpstream{}}
			req, err := http.NewRequest(http.MethodPost, srv.URL+"/backend-api/codex/responses", bytes.NewReader(plain))
			require.NoError(t, err)
			resp, err := svc.doOpenAIUpstream(req, "", zstdTestCodexAccount(t))
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			if optIn {
				require.Equal(t, http.StatusBadRequest, resp.StatusCode)
				require.JSONEq(t, `{"detail":"Bad Request"}`, string(body))
			} else {
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.Equal(t, "OK", string(body))
			}
			require.Equal(t, int32(1), calls.Load(), "no generic 400 retry")
		})
	}
}

func TestMaybeCompressCodexUpstreamRequest_CompressesResponses(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: codexCompressionTestConfig(t)}
	account := zstdTestCodexAccount(t)

	plain := []byte(`{"model":"gpt-5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`)
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	svc.maybeCompressCodexUpstreamRequest(req, account)

	if got := req.Header.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("Content-Encoding = %q, want zstd", got)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if int64(len(body)) != req.ContentLength {
		t.Fatalf("ContentLength = %d, body %d", req.ContentLength, len(body))
	}
	zr, err := zstd.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer zr.Close()
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decompressed body mismatch:\n got: %s\nwant: %s", got, plain)
	}

	// 401 重放：GetBody 必须返回相同的压缩字节。
	rc, err := req.GetBody()
	if err != nil {
		t.Fatalf("GetBody: %v", err)
	}
	replay, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("replay ReadAll: %v", err)
	}
	if !bytes.Equal(replay, body) {
		t.Fatal("GetBody replay bytes differ from first Body read")
	}
}

func TestMaybeCompressCodexUpstreamRequest_SkipsNonResponsesPath(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: codexCompressionTestConfig(t)}
	account := zstdTestCodexAccount(t)

	for _, path := range []string{
		"https://chatgpt.com/backend-api/codex/responses/count_tokens",
		"https://chatgpt.com/backend-api/codex/images/generations",
		"https://chatgpt.com/v1/embeddings",
	} {
		plain := []byte(`{"a":1}`)
		req, err := http.NewRequest(http.MethodPost, path, bytes.NewReader(plain))
		if err != nil {
			t.Fatalf("NewRequest %s: %v", path, err)
		}
		svc.maybeCompressCodexUpstreamRequest(req, account)
		if got := req.Header.Get("Content-Encoding"); got != "" {
			t.Fatalf("%s: Content-Encoding = %q, want empty", path, got)
		}
		body, _ := io.ReadAll(req.Body)
		if !bytes.Equal(body, plain) {
			t.Fatalf("%s: body mutated", path)
		}
	}
}

func TestMaybeCompressCodexUpstreamRequest_SkipsNonCodexAndDisabled(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: codexCompressionTestConfig(t)}

	apiKeyAccount := &Account{ID: 1, Type: AccountTypeAPIKey, Platform: PlatformOpenAI}
	plain := []byte(`{"model":"gpt-5","input":[]}`)

	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	svc.maybeCompressCodexUpstreamRequest(req, apiKeyAccount)
	if got := req.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("api-key account Content-Encoding = %q", got)
	}

	// 非 openai 平台的 OAuth 账号（如 grok）：UsesOpenAICodexProtocol 只按 Type 判真，
	// 但 zstd 是 Codex 专有压缩，xAI 等上游会 400 拒绝带它的请求。
	grokOAuth := &Account{ID: 2, Type: AccountTypeOAuth, Platform: PlatformGrok, Extra: map[string]any{}}
	reqGrok, err := http.NewRequest(http.MethodPost, "https://api.x.ai/v1/responses", bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	svc.maybeCompressCodexUpstreamRequest(reqGrok, grokOAuth)
	if got := reqGrok.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("grok oauth account Content-Encoding = %q, want empty", got)
	}

	// exact fidelity 关闭时，codex 账号也不压缩。
	svcOff := &OpenAIGatewayService{cfg: codexExactFidelityOffTestConfig(t)}
	svcOff.cfg.Gateway.OpenAIWS.CodexRequestCompressionEnabled = true
	req2, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	svcOff.maybeCompressCodexUpstreamRequest(req2, zstdTestCodexAccount(t))
	if got := req2.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("disabled config Content-Encoding = %q", got)
	}
}

func TestMaybeCompressCodexUpstreamRequest_PreservesExistingEncoding(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: codexCompressionTestConfig(t)}
	account := zstdTestCodexAccount(t)

	plain := []byte(`{"model":"gpt-5"}`)
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Encoding", "gzip")
	svc.maybeCompressCodexUpstreamRequest(req, account)
	if got := req.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	body, _ := io.ReadAll(req.Body)
	if !bytes.Equal(body, plain) {
		t.Fatal("body mutated despite existing Content-Encoding")
	}
}

func TestResolveCodexHTTPSTLSProfile_DefaultsAndOverrides(t *testing.T) {
	svc := &OpenAIGatewayService{}

	// Official Linux Codex CLI HTTP lane: OpenSSL, no ALPN, raw HTTP/1.1.
	if p := svc.resolveCodexHTTPSTLSProfile(zstdTestCodexAccount(t)); p == nil || !p.H1 || p.H2 || len(p.ALPNProtocols) != 0 || !strings.Contains(p.Name, "OpenSSL") {
		t.Fatalf("default profile = %+v, want openssl36 h1", p)
	}

	// Custom-CA deployment emulation: rustls + h2.
	h2Svc := &OpenAIGatewayService{cfg: &config.Config{}}
	h2Svc.cfg.Gateway.OpenAIWS.CodexHTTPH2Enabled = true
	if p := h2Svc.resolveCodexHTTPSTLSProfile(zstdTestCodexAccount(t)); p == nil || !p.H2 || !strings.Contains(strings.ToLower(p.Name), "rustls") {
		t.Fatalf("h2 profile = %+v, want rustls-h2", p)
	}

	withProfile := zstdTestCodexAccount(t)
	withProfile.Extra["codex_tls_profile"] = "nodejs24"
	if p := svc.resolveCodexHTTPSTLSProfile(withProfile); p == nil || !strings.Contains(strings.ToLower(p.Name), "node") {
		t.Fatalf("override profile = %+v, want nodejs", p)
	}

	withNone := zstdTestCodexAccount(t)
	withNone.Extra["codex_tls_profile"] = "none"
	if p := svc.resolveCodexHTTPSTLSProfile(withNone); p != nil {
		t.Fatalf("none profile = %+v, want nil", p)
	}
}

// codexExactFidelityTestConfig 构造开启 exact fidelity 的测试配置。
func codexExactFidelityTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.ExactFidelityEnabled = true
	return cfg
}

func codexExactFidelityOffTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{}
}
