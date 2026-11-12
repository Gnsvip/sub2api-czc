package httputil

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/userfacing"
	"go.uber.org/zap"
)

const (
	BodyReadKindNone                       = "none"
	BodyReadKindMaxBytes                   = "max_bytes"
	BodyReadKindUnsupportedContentEncoding = "unsupported_content_encoding"
	BodyReadKindDecodeContentEncoding      = "decode_content_encoding"
	BodyReadKindClientDisconnect           = "client_disconnect"
	BodyReadKindTruncatedBody              = "truncated_body"
	BodyReadKindReadTimeout                = "read_timeout"
	BodyReadKindTransport                  = "transport"
	BodyReadKindIORead                     = "io_read"
)

// RequestBodyReadError retains upload progress without retaining body bytes.
// Unwrap preserves the reader's error classification and body size limits.
type RequestBodyReadError struct {
	Err       error
	BytesRead int64
	Elapsed   time.Duration
}

func (e *RequestBodyReadError) Error() string { return e.Err.Error() }
func (e *RequestBodyReadError) Unwrap() error { return e.Err }

// RequestBodyReadLogFields contains only fixed categories and numeric metadata.
// BytesRead counts encoded bytes returned by the body reader, before decompression.
func RequestBodyReadLogFields(req *http.Request, err error) []zap.Field {
	contentLength := int64(-1)
	contentEncoding, protocol := "identity", "other"
	chunked := false
	if req != nil {
		contentLength = req.ContentLength
		contentEncoding = RequestContentEncodingCategory(req.Header.Get("Content-Encoding"))
		switch req.Proto {
		case "HTTP/1.0", "HTTP/1.1", "HTTP/2.0", "HTTP/3.0":
			protocol = req.Proto
		}
		for _, encoding := range req.TransferEncoding {
			if strings.EqualFold(encoding, "chunked") {
				chunked = true
			}
		}
	}
	fields := []zap.Field{
		zap.String("error_kind", RequestBodyReadErrorKind(err)),
		zap.String("content_encoding", contentEncoding),
		zap.Int64("content_length", contentLength),
		zap.String("protocol", protocol),
		zap.Bool("chunked", chunked),
	}
	var progress *RequestBodyReadError
	if errors.As(err, &progress) {
		fields = append(fields,
			zap.Int64("bytes_read", progress.BytesRead),
			zap.Int64("read_ms", progress.Elapsed.Milliseconds()),
		)
		if contentLength >= progress.BytesRead {
			fields = append(fields, zap.Int64("content_length_gap_bytes", contentLength-progress.BytesRead))
		}
	}
	return fields
}

// ExtractMaxBytesError unwraps a MaxBytesError if present.
func ExtractMaxBytesError(err error) (*http.MaxBytesError, bool) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return maxErr, true
	}
	return nil, false
}

// RequestContentEncodingCategory maps a Content-Encoding header to a small
// set of labels safe to log (no raw header values).
func RequestContentEncodingCategory(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "identity":
		return "identity"
	case "gzip", "x-gzip":
		return "gzip"
	case "zstd":
		return "zstd"
	case "deflate":
		return "deflate"
	default:
		return "other"
	}
}

// RequestBodyReadErrorKind classifies a body-read failure without including
// request payload. Used by operators to distinguish compression failures from
// a disconnected or truncated upload.
func RequestBodyReadErrorKind(err error) string {
	if err == nil {
		return BodyReadKindNone
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return BodyReadKindMaxBytes
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "decode content-encoding") {
		if strings.Contains(lower, "unsupported content-encoding") {
			return BodyReadKindUnsupportedContentEncoding
		}
		return BodyReadKindDecodeContentEncoding
	}
	var netErr net.Error
	if errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return BodyReadKindReadTimeout
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe) {
		return BodyReadKindClientDisconnect
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return BodyReadKindTruncatedBody
	}
	if errors.As(err, &netErr) {
		return BodyReadKindTransport
	}
	switch {
	case strings.Contains(lower, "i/o timeout"):
		return BodyReadKindTransport
	case strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "broken pipe"),
		strings.Contains(lower, "use of closed network connection"),
		strings.Contains(lower, "client disconnected"),
		strings.Contains(lower, "stream closed"):
		return BodyReadKindClientDisconnect
	}
	return BodyReadKindIORead
}

// RequestBodyReadIncomplete reports whether the failure is a client abort,
// truncated upload, or transport timeout rather than a malformed body.
func RequestBodyReadIncomplete(err error) bool {
	switch RequestBodyReadErrorKind(err) {
	case BodyReadKindClientDisconnect, BodyReadKindTruncatedBody, BodyReadKindReadTimeout, BodyReadKindTransport:
		return true
	default:
		return false
	}
}

// RequestBodyReadHTTPStatus maps a body-read error to the client HTTP status.
func RequestBodyReadHTTPStatus(err error) int {
	switch RequestBodyReadErrorKind(err) {
	case BodyReadKindNone:
		return http.StatusOK
	case BodyReadKindMaxBytes:
		return http.StatusRequestEntityTooLarge
	case BodyReadKindClientDisconnect, BodyReadKindTruncatedBody, BodyReadKindReadTimeout, BodyReadKindTransport:
		return http.StatusRequestTimeout
	default:
		return http.StatusBadRequest
	}
}

// RequestBodyReadErrorMessage is the user-facing message for a classified
// body-read failure. Callers that know a MaxBytesError limit should replace
// the max-bytes message with a limit-specific one.
func RequestBodyReadErrorMessage(err error) string {
	switch RequestBodyReadErrorKind(err) {
	case BodyReadKindMaxBytes:
		return "Request body is too large"
	case BodyReadKindClientDisconnect, BodyReadKindTruncatedBody, BodyReadKindReadTimeout, BodyReadKindTransport:
		return userfacing.RequestBodyIncomplete
	default:
		return userfacing.FailedToReadBody
	}
}
