package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func (s *OpenAIGatewayService) SetBPSProxyRepository(repo ProxyRepository) { s.bpsProxyRepo = repo }

// The admin connectivity check follows the same BPS credential, proxy and
// response validation path. Its bounded capture translates the final answer to
// the existing admin test event format without sending Excel tokens to Codex.
func (s *AccountTestService) testBPSAccountConnection(c *gin.Context, account *Account, model, prompt, mode string) error {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	if s.openaiGatewayService == nil {
		return s.sendErrorAndEnd(c, "BPS gateway is unavailable")
	}
	if mode == AccountTestModeCompact || isOpenAIImageModel(model) {
		return s.sendErrorAndEnd(c, "BPS connection test supports text Responses only")
	}
	if model == "" {
		model = openai.DefaultTestModel
		if models, err := bpsModelList(account.Extra[OpenAIExcelBPSModelsKey]); err == nil && len(models) > 0 {
			model = models[0]
		}
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Reply with OK."
	}
	payload := map[string]any{"model": model, "input": prompt, "stream": false}
	applyIntelligentPayloadPrompt(c.Request.Context(), payload)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to encode BPS test request")
	}
	s.sendEvent(c, TestEvent{Type: "test_start", Model: model})
	internal := c.Copy()
	capture := &responsesWebSearchRoundWriter{header: make(http.Header), status: http.StatusOK, size: -1}
	internal.Writer = capture
	_, err = s.openaiGatewayService.forwardBPS(s.withBPSTestEvidence(c.Request.Context(), c), internal, account, encoded, time.Now())
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	if capture.err != nil {
		return s.sendErrorAndEnd(c, "BPS test response exceeded the buffer limit")
	}
	for _, item := range gjson.GetBytes(capture.body.Bytes(), "output").Array() {
		for _, part := range item.Get("content").Array() {
			if part.Get("type").String() == "output_text" {
				s.sendEvent(c, TestEvent{Type: "content", Text: part.Get("text").String()})
			}
		}
	}
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

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

func (s *OpenAIGatewayService) forwardBPS(ctx context.Context, c *gin.Context, account *Account, body []byte, start time.Time) (*OpenAIForwardResult, error) {
	if !account.IsExcelBPSEnabled() || !s.excelBPSGloballyEnabled(ctx) {
		return nil, bpsRequestError(c, 503, "bps_disabled", "Enable BPS before use")
	}
	if !account.IsExcelBPSEnabledForModel(gjson.GetBytes(body, "model").String()) {
		return nil, bpsRequestError(c, 400, "bps_model_not_enabled", "This model is not enabled for BPS on this account")
	}
	if GetOpenAIClientTransport(c) == OpenAIClientTransportWS {
		return nil, bpsRequestError(c, 400, "bps_http_required", "BPS requires HTTP Responses")
	}
	return s.forwardExcelBPS(ctx, c, account, body, start)
}
