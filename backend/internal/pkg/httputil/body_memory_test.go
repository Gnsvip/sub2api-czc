package httputil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"testing/iotest"
	"time"
)

type bodyReaderOnly struct{ io.Reader }

func TestReadRequestBodyChunksPreservesContent(t *testing.T) {
	for _, size := range []int{0, 1, 512, 513, 1 << 20, (2 << 20) + 17} {
		body := bytes.Repeat([]byte{'x'}, size)
		for _, declared := range []int64{-1, 0, 1, int64(size), 1 << 40} {
			t.Run(fmt.Sprintf("size_%d_length_%d", size, declared), func(t *testing.T) {
				req := &http.Request{Body: io.NopCloser(bodyReaderOnly{bytes.NewReader(body)}), ContentLength: declared, Header: make(http.Header)}
				got, err := ReadRequestBodyWithPrealloc(req)
				if err != nil || !bytes.Equal(got, body) {
					t.Fatalf("body was lost or changed: %v", err)
				}
			})
		}
	}
}

func TestReadRequestBodyChunksPreservesReadErrors(t *testing.T) {
	for _, readErr := range []error{io.ErrUnexpectedEOF, errors.New("connection reset")} {
		reader := io.MultiReader(bytes.NewReader([]byte("partial")), iotest.ErrReader(readErr))
		req := &http.Request{Body: io.NopCloser(reader), ContentLength: 100, Header: make(http.Header)}
		got, err := ReadRequestBodyWithPrealloc(req)
		if !errors.Is(err, readErr) || got != nil {
			t.Fatalf("truncated body accepted: body=%q err=%v", got, err)
		}
		var progress *RequestBodyReadError
		if !errors.As(err, &progress) || progress.BytesRead != 7 || progress.Elapsed < 0 {
			t.Fatalf("missing upload progress: %#v", progress)
		}
	}
}

type partialBodyFailureReader struct{ remaining int }

func (r *partialBodyFailureReader) Read(p []byte) (int, error) {
	n := min(len(p), r.remaining)
	clear(p[:n])
	r.remaining -= n
	if r.remaining == 0 {
		return n, io.ErrUnexpectedEOF
	}
	return n, nil
}

func TestReadRequestBodyChunksCountsBytesReturnedWithError(t *testing.T) {
	for _, size := range []int{0, 7, 513, (2 << 20) + 17} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			req := &http.Request{Body: io.NopCloser(&partialBodyFailureReader{remaining: size}), ContentLength: int64(size + 100)}
			started := time.Now()
			body, err := ReadRequestBodyWithPrealloc(req)
			var progress *RequestBodyReadError
			if body != nil || !errors.Is(err, io.ErrUnexpectedEOF) || !errors.As(err, &progress) {
				t.Fatalf("partial upload accepted or error lost: %v", err)
			}
			if progress.BytesRead != int64(size) || progress.Elapsed < 0 || progress.Elapsed > time.Since(started) {
				t.Fatalf("wrong progress across chunks/final read: %#v", progress)
			}
		})
	}
}

func TestReadRequestBodyChunksPreservesMaxBytesReader(t *testing.T) {
	body := bytes.Repeat([]byte{'x'}, (2<<20)+1)
	req := &http.Request{Body: http.MaxBytesReader(nil, io.NopCloser(bytes.NewReader(body)), 2<<20), ContentLength: int64(len(body)), Header: make(http.Header)}
	got, err := ReadRequestBodyWithPrealloc(req)
	var limitErr *http.MaxBytesError
	if !errors.As(err, &limitErr) || limitErr.Limit != 2<<20 || got != nil {
		t.Fatalf("body limit changed: len=%d err=%v", len(got), err)
	}
	var progress *RequestBodyReadError
	if !errors.As(err, &progress) || progress.BytesRead != 2<<20 {
		t.Fatalf("max bytes progress changed: %#v", progress)
	}
}

func BenchmarkReadLargeBody(b *testing.B) {
	for _, size := range []int{512 << 10, 69 << 20} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			body := bytes.Repeat([]byte{'x'}, size)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req := &http.Request{Body: io.NopCloser(bodyReaderOnly{bytes.NewReader(body)}), ContentLength: int64(size), Header: make(http.Header)}
				got, err := ReadRequestBodyWithPrealloc(req)
				if err != nil || !bytes.Equal(got, body) {
					b.Fatalf("body changed: %v", err)
				}
			}
		})
	}
}
