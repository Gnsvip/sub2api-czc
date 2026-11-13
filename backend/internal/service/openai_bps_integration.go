package service

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"time"
)

type excelBPSGinContextKey struct{}

// Keep BPS observations on this fork's HTTP timing and diagnostic pipeline.
func (s *OpenAIGatewayService) doExcelBPSObservedHTTP(req *http.Request, proxy string, account *Account) (*http.Response, error) {
	c, _ := req.Context().Value(excelBPSGinContextKey{}).(*gin.Context)
	if c == nil {
		return s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	}
	traceOpenAIUpstream(req.Context(), c, account, c.GetInt("excel_bps_upstream_attempt"), req)
	timing := newOpenAIForwardTiming(c, req, account)
	req = req.WithContext(timing.context(req.Context()))
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	timing.markResponse(resp, err)
	traceOpenAIUpstreamResult(c, account, c.GetInt("excel_bps_upstream_attempt"), resp, err)
	return resp, err
}
func (s *OpenAIGatewayService) getExcelBPSAccessToken(ctx context.Context, account *Account) (string, error) {
	if grants := s.excelGrants(); grants != nil && !account.IsExcelOAuth() {
		return grants.ExcelAccessToken(ctx, account, s.openAITokenProvider.tokenCache)
	}
	token, _, err := s.GetAccessToken(ctx, account)
	return token, err
}
func (s *OpenAIGatewayService) excelGrants() *OpenAIExcelOAuthService {
	if s == nil || s.openAITokenProvider == nil || s.openAITokenProvider.openAIOAuthService == nil {
		return nil
	}
	return s.openAITokenProvider.openAIOAuthService.excelGrants
}
func (a *Account) IsExcelOAuth() bool { return a.isBPSCredential() }
func QualityBPSEligible(a *Account) bool {
	return a != nil && a.Platform == PlatformOpenAI && a.Type == AccountTypeOAuth && !a.IsOpenAIAgentIdentity() && !strings.EqualFold(strings.TrimSpace(a.GetCredential("plan_type")), "free")
}

// Explicit admin probes observe BPS availability without mutating account health.
func isQualityObservation(ctx context.Context) bool {
	_, ok := ctx.Value(bpsTestResponseObserverKey{}).(bpsTestResponseObserver)
	return ok
}

const OpsClientCanceledCode = "client_canceled"

type AccountExcelBPSRepository interface {
	DisableExcelBPSOn403(ctx context.Context, account *Account) (bool, error)
}
type AccountExcelBPSGroupRepository interface {
	MoveExcelBPSOn403(ctx context.Context, account *Account) (bool, error)
}

func MarkOpsClientCancellation(c *gin.Context, stream bool) {
	MarkOpsStreamErrorValue(c, OpsStreamError{
		ErrType:        OpsClientCanceledCode,
		Code:           OpsClientCanceledCode,
		Message:        "Client disconnected: context canceled",
		IntendedStatus: 499,
		RequestScoped:  true,
		NonStream:      !stream,
	})
}
func writeOpenAICompactSSEFailureMessageParam(c *gin.Context, statusCode int, errType, message, param string) {
	if c == nil {
		return
	}
	MarkOpsStreamError(c, errType, message, statusCode)
	payload, err := json.Marshal(map[string]any{
		"type":            "response.failed",
		"sequence_number": 0,
		"response": map[string]any{
			"id":     "resp_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			"object": "response",
			// 严格客户端把 created_at 当必填字段，缺失会反序列化失败，
			// 终止事件就白发了（退化成盲重连）。与 writeResponsesFailedSSE 对齐。
			"created_at": time.Now().Unix(),
			"status":     "failed",
			"output":     []any{},
			"error": func() map[string]any {
				errorBody := map[string]any{
					"code":    errType,
					"message": message,
				}
				if param != "" {
					errorBody["param"] = param
				}
				return errorBody
			}(),
		},
	})
	if err != nil {
		return
	}
	_, _ = c.Writer.Write([]byte("event: response.failed\ndata: "))
	_, _ = c.Writer.Write(payload)
	_, _ = c.Writer.Write([]byte("\n\n"))
	c.Writer.Flush()
}

func (s *OpenAIGatewayService) excelBPSGloballyEnabled(ctx context.Context) bool {
	if s == nil || s.settingService == nil || s.settingService.settingRepo == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, gatewayForwardingDBTimeout)
	defer cancel()
	value, err := s.settingService.settingRepo.GetValue(ctx, SettingKeyExcelBPSEnabled)
	return err == nil && value == "true"
}
