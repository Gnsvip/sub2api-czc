package httputil

import (
	"context"
	"errors"
	"net/http"

	"go.uber.org/zap"
)

const (
	RequestBodyReadErrorMaxBytes                   = BodyReadKindMaxBytes
	RequestBodyReadErrorTimeout                    = BodyReadKindReadTimeout
	RequestBodyReadErrorClientDisconnect           = BodyReadKindClientDisconnect
	RequestBodyReadErrorTruncated                  = BodyReadKindTruncatedBody
	RequestBodyReadErrorDecodeContentEncoding      = BodyReadKindDecodeContentEncoding
	RequestBodyReadErrorUnsupportedContentEncoding = BodyReadKindUnsupportedContentEncoding
	RequestBodyReadErrorTransport                  = BodyReadKindTransport
	RequestBodyReadErrorIO                         = BodyReadKindIORead
	StatusClientClosedRequest                      = 499
)

// RequestBodyReadFailure shares the upstream ingress contract while preserving
// this fork's incomplete-upload response and bounded progress diagnostics.
type RequestBodyReadFailure struct {
	Kind          string
	Status        int
	Message       string
	MaxBytesLimit int64
	fields        []zap.Field
}

func DescribeRequestBodyReadFailure(req *http.Request, err error) RequestBodyReadFailure {
	f := RequestBodyReadFailure{
		Kind:    RequestBodyReadErrorKind(err),
		Status:  RequestBodyReadHTTPStatus(err),
		Message: RequestBodyReadErrorMessage(err),
		fields:  RequestBodyReadLogFields(req, err),
	}
	if maxErr, ok := ExtractMaxBytesError(err); ok {
		f.MaxBytesLimit = maxErr.Limit
	}
	// An HTTP/1 read deadline may also cancel the request context. Classify
	// the timeout first so it remains a 408, not a client-disconnect 499.
	if f.Kind == BodyReadKindClientDisconnect || (err != nil && !IsRequestBodyReadTimeout(err) && req != nil && errors.Is(req.Context().Err(), context.Canceled) && (f.Kind == BodyReadKindTruncatedBody || f.Kind == BodyReadKindIORead || f.Kind == BodyReadKindTransport)) {
		f.Kind = BodyReadKindClientDisconnect
		f.Status = StatusClientClosedRequest
	}
	for i := range f.fields {
		if f.fields[i].Key == "error_kind" {
			f.fields[i] = zap.String("error_kind", f.Kind)
		}
	}
	return f
}

func (f RequestBodyReadFailure) LogFields() []zap.Field {
	return append(f.fields, zap.Int("status", f.Status))
}

func IsRequestBodyReadTimeout(err error) bool {
	return RequestBodyReadErrorKind(err) == BodyReadKindReadTimeout
}
