package handler

import (
	"net/http"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// logRequestBodyReadFailure records a bounded, payload-free reason for a body
// read failure. Clients receive a classified status/message; operators get
// enough information to distinguish compression failures from a
// disconnected/truncated upload without logging request content.
func logRequestBodyReadFailure(reqLog *zap.Logger, req *http.Request, err error) {
	if err == nil {
		return
	}
	if reqLog == nil {
		if req != nil {
			reqLog = logger.FromContext(req.Context())
		} else {
			reqLog = logger.L()
		}
	}

	reqLog.Warn("read request body failed", pkghttputil.DescribeRequestBodyReadFailure(req, err).LogFields()...)
}

type gatewayErrorWriter func(*gin.Context, int, string, string)

// writeRequestBodyReadFailure logs the bounded failure reason and writes the
// classified client response (413 / 408 / 400).
func writeRequestBodyReadFailure(c *gin.Context, reqLog *zap.Logger, err error, write gatewayErrorWriter) {
	if write == nil {
		return
	}
	if handleRequestBodyReadError(c, reqLog, err) {
		return
	}
	if maxErr, ok := pkghttputil.ExtractMaxBytesError(err); ok {
		write(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
		return
	}
	var req *http.Request
	if c != nil {
		req = c.Request
	}
	status, message := requestBodyReadFailure(reqLog, req, err)
	write(c, status, "invalid_request_error", message)
}

func requestBodyReadFailure(reqLog *zap.Logger, req *http.Request, err error) (int, string) {
	failure := pkghttputil.DescribeRequestBodyReadFailure(req, err)
	if failure.Kind == pkghttputil.RequestBodyReadErrorMaxBytes {
		return failure.Status, buildBodyTooLargeMessage(failure.MaxBytesLimit)
	}
	logRequestBodyReadFailure(reqLog, req, err)
	return failure.Status, failure.Message
}
