package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service/basispoints"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func (s *OpenAIGatewayService) SetBPSProxyRepository(repo ProxyRepository) { s.bpsProxyRepo = repo }

func bpsRequestScope(c *gin.Context, accountID int64, body []byte) string {
	keyID := getAPIKeyIDFromContext(c)
	identity, _ := resolveOpenAIWSExecutionScope(c, body, keyID)
	if identity == "" {
		identity = openAIWSLocalStateScope(c, keyID)
	}
	return fmt.Sprintf("bps/account:%d/key:%d/session:%s", accountID, keyID, identity)
}

// Never return a failover signal from this route. Even a lost response might
// represent a completed upstream turn; another account/IP would duplicate it.
func bpsRequestError(c *gin.Context, status int, code, message string) error {
	StopOpenAICompactSSEKeepaliveCommitted(c)
	MarkResponseCommitted(c)
	if !c.Writer.Written() {
		c.Header("Content-Type", "application/json")
		c.JSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "code": code, "message": message}})
	} else if c.Writer.Header().Get("Content-Type") == "text/event-stream" {
		writeOpenAICompactSSEFailureMessage(c, status, code, message)
	}
	return fmt.Errorf("BPS request failed: %s", code)
}

func bpsUnsupportedHistory(value gjson.Result) bool {
	if value.IsObject() {
		if value.Get("encrypted_content").Exists() || value.Get("type").String() == "compaction" || value.Get("type").String() == "compaction_trigger" {
			return true
		}
	}
	blocked := false
	if value.IsArray() || value.IsObject() {
		value.ForEach(func(_, child gjson.Result) bool { blocked = bpsUnsupportedHistory(child); return !blocked })
	}
	return blocked
}

func (s *OpenAIGatewayService) forwardBPS(ctx context.Context, c *gin.Context, account *Account, body []byte, start time.Time) (*OpenAIForwardResult, error) {
	fail := func(status int, code, message string) (*OpenAIForwardResult, error) {
		return nil, bpsRequestError(c, status, code, message)
	}
	if !account.isBPSEnabled() {
		return fail(503, "bps_disabled", "Enable BPS on this Excel OAuth account before use")
	}
	model := gjson.GetBytes(body, "model").String()
	if !account.bpsModelAllowed(model) {
		return fail(400, "bps_model_not_enabled", "This model is not enabled for BPS on this account")
	}
	if GetOpenAIClientTransport(c) == OpenAIClientTransportWS || isOpenAIResponsesCompactPath(c) || bpsUnsupportedHistory(gjson.GetBytes(body, "input")) {
		return fail(400, "bps_unsupported_request", "BPS requires HTTP Responses with complete plaintext history; WebSocket and native compaction are not supported")
	}
	if basispoints.NativeFallbackReason(body) != "" {
		return fail(400, "bps_unsupported_tool", "This hosted tool requires the native Codex route")
	}
	scope := bpsRequestScope(c, account.ID, body)
	upstreamBody, bridge, err := basispoints.Prepare(body, scope, &s.bpsReplay)
	if err != nil {
		return fail(400, "bps_request_invalid", "BPS cannot translate this request; use complete history and supported text/function tools")
	}
	if len(bridge.Warnings) > 0 {
		return fail(400, "bps_unsupported_tool", "BPS cannot preserve all declared tools")
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return fail(502, "bps_auth_unavailable", "Excel OAuth credential is unavailable")
	}
	accountID := account.GetChatGPTAccountID()
	if accountID == "" {
		return fail(400, "bps_account_id_missing", "Excel OAuth requires chatgpt_account_id")
	}

	proxyURL := ""
	selectedAccount := *account
	if pooled, _ := account.Extra[OpenAIExcelBPSSessionProxyKey].(bool); pooled {
		if s.bpsProxyRepo == nil {
			return fail(503, "bps_proxy_unavailable", "BPS session proxy pool is unavailable")
		}
		proxies, listErr := s.bpsProxyRepo.ListActive(ctx)
		if listErr != nil {
			return fail(503, "bps_proxy_unavailable", "BPS session proxy pool is unavailable")
		}
		proxy, release, leaseErr := s.bpsSessions.acquire(scope, proxies, time.Now())
		if leaseErr != nil {
			return fail(503, "bps_proxy_unavailable", "No enabled, unexpired BPS exit is available; direct fallback is disabled")
		}
		selectedAccount.Proxy, selectedAccount.ProxyID = proxy, &proxy.ID
		proxyURL = proxy.URL()
		// Defers added below close the response before releasing the lease.
		failed := false
		defer func() { release(failed) }()
		return s.sendBPS(ctx, c, &selectedAccount, upstreamBody, bridge, model, gjson.GetBytes(body, "stream").Bool(), token, accountID, proxyURL, start, &failed)
	}
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	return s.sendBPS(ctx, c, &selectedAccount, upstreamBody, bridge, model, gjson.GetBytes(body, "stream").Bool(), token, accountID, proxyURL, start, new(bool))
}

func (s *OpenAIGatewayService) sendBPS(ctx context.Context, c *gin.Context, account *Account, body []byte, bridge *basispoints.Bridge, model string, stream bool, token, accountID, proxyURL string, start time.Time, proxyFailed *bool) (*OpenAIForwardResult, error) {
	requestCtx, cancel := context.WithCancel(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileBPS)))
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, basispoints.ResponsesURL, bytes.NewReader(body))
	if err != nil {
		return nil, bpsRequestError(c, 400, "bps_request_invalid", "Invalid BPS request")
	}
	req.Header = http.Header{
		"Authorization": {"Bearer " + token}, "Chatgpt-Account-Id": {accountID}, "X-Openai-Account-Id": {accountID},
		"X-Basispoints-Auth-Mode": {"chatgpt"}, "Content-Type": {"application/json"}, "Accept": {"text/event-stream"},
		"Origin": {"https://bps.openai.com"}, "User-Agent": {"Mozilla/5.0"},
		"X-Openai-Internal-Basispoints-Client-Product":       {"basispoints-excel-plugin"},
		"X-Openai-Internal-Basispoints-Client-Agent-Profile": {"excel"},
	}
	SetActualOpenAIUpstreamEndpoint(c, "/basispoints/api/responses")
	SetOpsUpstreamModel(c, model)
	MarkOpenAIResponsesClientStream(c, stream)
	traceOpenAIUpstream(ctx, c, account, 1, req)
	timing := newOpenAIForwardTiming(c, req, account)
	req = req.WithContext(timing.context(req.Context()))
	stopKeepalive := func() {}
	if stream && s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		stopKeepalive = startOpenAISSEKeepalive(c, time.Duration(s.cfg.Gateway.StreamKeepaliveInterval)*time.Second)
	}
	sent := time.Now()
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	stopKeepalive()
	timing.markResponse(resp, err)
	traceOpenAIUpstreamResult(c, account, 1, resp, err)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(sent).Milliseconds())
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		*proxyFailed = ctx.Err() == nil
		return nil, bpsRequestError(c, 502, "bps_transport_error", "BPS connection failed; the request was not replayed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		*proxyFailed = resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504
		message := fmt.Sprintf("Excel BPS returned HTTP %d; the request was not replayed", resp.StatusCode)
		setOpsUpstreamError(c, resp.StatusCode, message, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Platform: account.Platform, AccountID: account.ID, AccountName: account.Name, ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account), UpstreamStatusCode: resp.StatusCode, UpstreamRequestID: resp.Header.Get("x-request-id"), UpstreamURL: basispoints.ResponsesURL, Kind: "http_error", Message: message})
		status := resp.StatusCode
		if status < 400 {
			status = 502
		}
		return nil, bpsRequestError(c, status, "bps_upstream_error", message)
	}
	converted := bridge.Stream(resp.Body)
	defer converted.Close()
	translated := *resp
	translated.Body = converted
	translated.Header = resp.Header.Clone()
	translated.Header.Set("Content-Type", "text/event-stream")
	translated.Header.Del("Content-Length")
	translated.ContentLength = -1
	result := &OpenAIForwardResult{RequestID: resp.Header.Get("x-request-id"), UpstreamHeaders: resp.Header, Model: model, BillingModel: model, UpstreamModel: model, UpstreamEndpoint: "/basispoints/api/responses", Stream: stream, ReasoningEffort: &bridge.Effort, RequestedReasoningEffort: coalesceRequestedReasoningEffort(RequestedReasoningEffortFromContext(ctx), &bridge.RequestedEffort)}
	if stream {
		var output *openaiStreamingResult
		output, err = s.handleStreamingResponseWithReasoning(ctx, &translated, c, account, start, model, model, bridge.Effort)
		if output != nil {
			if output.usage != nil {
				result.Usage = *output.usage
			}
			result.FirstTokenMs, result.ResponseID = output.firstTokenMs, output.responseID
		}
	} else {
		var output *openaiNonStreamingResult
		output, err = s.handleNonStreamingResponse(ctx, &translated, c, account, model, model)
		if output != nil {
			if output.usage != nil {
				result.Usage = *output.usage
			}
			result.ResponseID = output.responseID
		}
	}
	result.Duration = time.Since(start)
	result.UpstreamResponseModel = observedUpstreamResponseModel(c)
	result.UpstreamResponseModelConflict = observedUpstreamResponseModelConflict(c)
	result.UpstreamResponseServiceTier = observedUpstreamResponseServiceTier(c)
	if err != nil {
		result.ClientDisconnect = ctx.Err() != nil
		// Deliberately do not wrap err: shared response processing can return a
		// failover marker, which must not escape the BPS no-replay boundary.
		return result, bpsRequestError(c, 502, "bps_response_failed", "BPS response did not complete; the request was not replayed")
	}
	s.bindHTTPResponseAccount(ctx, c, account, result.ResponseID)
	return result, nil
}
