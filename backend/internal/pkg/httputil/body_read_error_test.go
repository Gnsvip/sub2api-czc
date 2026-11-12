package httputil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"
)

func TestRequestBodyReadErrorKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		kind string
	}{
		{name: "nil", err: nil, kind: BodyReadKindNone},
		{name: "max bytes", err: &http.MaxBytesError{Limit: 10}, kind: BodyReadKindMaxBytes},
		{name: "unsupported encoding", err: errors.New(`decode Content-Encoding "br": unsupported Content-Encoding`), kind: BodyReadKindUnsupportedContentEncoding},
		{name: "decode encoding", err: errors.New(`decode Content-Encoding "gzip": unexpected EOF`), kind: BodyReadKindDecodeContentEncoding},
		{name: "canceled", err: context.Canceled, kind: BodyReadKindClientDisconnect},
		{name: "reset", err: syscall.ECONNRESET, kind: BodyReadKindClientDisconnect},
		{name: "pipe", err: syscall.EPIPE, kind: BodyReadKindClientDisconnect},
		{name: "net closed", err: net.ErrClosed, kind: BodyReadKindClientDisconnect},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, kind: BodyReadKindTruncatedBody},
		{name: "wrapped reset", err: fmt.Errorf("read: %w", syscall.ECONNRESET), kind: BodyReadKindClientDisconnect},
		{name: "http2 stream closed", err: errors.New("http2: stream closed"), kind: BodyReadKindClientDisconnect},
		{name: "io timeout string", err: errors.New("read tcp 127.0.0.1:8080: i/o timeout"), kind: BodyReadKindTransport},
		{name: "read deadline", err: os.ErrDeadlineExceeded, kind: BodyReadKindReadTimeout},
		{name: "net timeout", err: timeoutNetError{}, kind: BodyReadKindReadTimeout},
		{name: "wrapped deadline", err: &RequestBodyReadError{Err: os.ErrDeadlineExceeded, BytesRead: 42}, kind: BodyReadKindReadTimeout},
		{name: "generic io", err: errors.New("read failed"), kind: BodyReadKindIORead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequestBodyReadErrorKind(tt.err); got != tt.kind {
				t.Fatalf("kind=%s want %s", got, tt.kind)
			}
		})
	}
}

func TestRequestBodyReadHTTPStatus(t *testing.T) {
	t.Parallel()
	if got := RequestBodyReadHTTPStatus(&http.MaxBytesError{Limit: 1}); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("max bytes status=%d", got)
	}
	if got := RequestBodyReadHTTPStatus(io.ErrUnexpectedEOF); got != http.StatusRequestTimeout {
		t.Fatalf("truncated status=%d", got)
	}
	if got := RequestBodyReadHTTPStatus(syscall.ECONNRESET); got != http.StatusRequestTimeout {
		t.Fatalf("disconnect status=%d", got)
	}
	if got := RequestBodyReadHTTPStatus(&RequestBodyReadError{Err: os.ErrDeadlineExceeded}); got != http.StatusRequestTimeout {
		t.Fatalf("wrapped timeout status=%d", got)
	}
	if got := RequestBodyReadHTTPStatus(errors.New("read failed")); got != http.StatusBadRequest {
		t.Fatalf("io status=%d", got)
	}
}

func TestRequestContentEncodingCategory(t *testing.T) {
	t.Parallel()
	if got := RequestContentEncodingCategory("private-payload-marker"); got != "other" {
		t.Fatalf("got %q", got)
	}
	if got := RequestContentEncodingCategory("gzip"); got != "gzip" {
		t.Fatalf("got %q", got)
	}
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

func TestRequestBodyReadIncomplete(t *testing.T) {
	t.Parallel()
	if !RequestBodyReadIncomplete(timeoutNetError{}) {
		t.Fatal("transport timeout should be incomplete")
	}
	if RequestBodyReadIncomplete(errors.New("decode Content-Encoding \"gzip\": boom")) {
		t.Fatal("decode failure is a malformed body, not an incomplete upload")
	}
}

func TestRequestBodyReadLogFieldsProgressAndPrivacy(t *testing.T) {
	for _, length := range []int64{-1, 0, 2, 100} {
		req := &http.Request{ContentLength: length, Proto: "secret-protocol-marker", Header: http.Header{"Content-Encoding": {"secret-encoding-marker"}}, TransferEncoding: []string{"chunked", "secret-transfer-marker"}}
		err := fmt.Errorf("secret-outer-marker: %w", &RequestBodyReadError{Err: fmt.Errorf("secret-body-marker: %w", io.ErrUnexpectedEOF), BytesRead: 7, Elapsed: 24757 * time.Millisecond})
		encoder := zapcore.NewMapObjectEncoder()
		for _, field := range RequestBodyReadLogFields(req, err) {
			field.AddTo(encoder)
		}
		fields := encoder.Fields
		if fields["bytes_read"] != int64(7) || fields["read_ms"] != int64(24757) || fields["chunked"] != true || fields["error_kind"] != BodyReadKindTruncatedBody {
			t.Fatalf("missing progress: %v", fields)
		}
		gap, present := fields["content_length_gap_bytes"]
		if present != (length >= 7) || (present && gap != length-7) {
			t.Fatalf("invalid remaining length %d: %v", length, fields)
		}
		if fields["protocol"] != "other" || fields["content_encoding"] != "other" || strings.Contains(fmt.Sprint(fields), "secret-") {
			t.Fatalf("unbounded or private log field: %v", fields)
		}
	}
	encoder := zapcore.NewMapObjectEncoder()
	for _, field := range RequestBodyReadLogFields(nil, io.ErrUnexpectedEOF) {
		field.AddTo(encoder)
	}
	if _, present := encoder.Fields["bytes_read"]; present {
		t.Fatal("unobserved byte count must not be presented as zero")
	}
}
