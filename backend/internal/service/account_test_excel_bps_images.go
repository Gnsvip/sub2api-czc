package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http/httptest"
	"time"
)

func (s *AccountTestService) testExcelBPSImages(c *gin.Context, ctx context.Context, account *Account, parsed *OpenAIImagesRequest, upstreamModel string) (bool, error) {
	if excelBPSImagesUnsupportedReason(parsed) != "" {
		return false, nil
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: fmt.Sprintf("Calling Excel BPS /images/generations; image model: %s\n", upstreamModel)})
	probe := httptest.NewRecorder()
	probeCtx, _ := gin.CreateTestContext(probe)
	probeCtx.Request = c.Request.Clone(s.withBPSTestEvidence(ctx, c))
	probeCtx.Request.Header.Set("Session-Id", "image-test-"+uuid.NewString())
	ctx = probeCtx.Request.Context()
	_, fallback, err := s.openaiGatewayService.forwardExcelBPSImages(ctx, probeCtx, account, parsed, parsed.Model, upstreamModel, time.Now())
	if fallback {
		s.sendEvent(c, TestEvent{Type: "content", Text: "Excel BPS rejected this request format; user requests fall back to Codex, testing Codex\n"})
		return false, nil
	}
	if err != nil {
		// A single-account test has no other account to fail over to.
		var upErr *OpenAIImagesUpstreamError
		if errors.As(err, &upErr) {
			return true, s.sendErrorAndEnd(c, upErr.clientMessage())
		}
		var failover *UpstreamFailoverError
		if errors.As(err, &failover) && failover.ClientMessage != "" {
			return true, s.sendErrorAndEnd(c, failover.ClientMessage)
		}
		return true, s.sendErrorAndEnd(c, err.Error())
	}
	results, err := parseCodexDirectImagesResponse(probe.Body.Bytes())
	if err != nil {
		return true, s.sendErrorAndEnd(c, fmt.Sprintf("Failed to parse Excel BPS image response: %s", err.Error()))
	}
	for _, item := range results {
		if item.RevisedPrompt != "" {
			s.sendEvent(c, TestEvent{Type: "content", Text: item.RevisedPrompt})
		}
		mimeType := openAIImageOutputMIMEType(item.OutputFormat)
		s.sendEvent(c, TestEvent{
			Type:     "image",
			ImageURL: "data:" + mimeType + ";base64," + item.Result,
			MimeType: mimeType,
		})
	}
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return true, nil
}
