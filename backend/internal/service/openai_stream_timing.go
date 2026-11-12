package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type openAIStreamTimingContextKey struct{}

const (
	streamTimingRaw = iota
	streamTimingDecoded
	streamTimingWrite
	streamTimingFlush
	streamTimingIOCount
)

// Fixed storage, independent of stream length, payload size and event names.
// The last five-second activity bucket aggregates everything after 115 seconds.
const streamTimingBuckets = 24
const streamTimingBucketWidth = 5 * time.Second

var streamTimingEventNames = [...]string{
	"other", "comment", "response.created", "response.queued", "response.in_progress",
	"response.output_item.added", "response.output_item.done",
	"response.content_part.added", "response.content_part.done",
	"response.output_text.delta", "response.output_text.done",
	"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
	"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done",
	"response.reasoning_text.delta", "response.reasoning_text.done",
	"response.function_call_arguments.delta", "response.function_call_arguments.done",
	"response.custom_tool_call_input.delta", "response.custom_tool_call_input.done",
	"response.refusal.delta", "response.refusal.done",
	"response.image_generation_call.partial_image", "response.completed", "response.done",
	"response.failed", "error", "[DONE]",
}

type streamTimingIO struct {
	calls, bytes, failures              int64
	first, last, maxGapStart, maxGapEnd time.Duration
	maxCallStart, maxCallEnd            time.Duration
	progress                            bool
}

func (s *streamTimingIO) record(start, end time.Duration, n int, err error, progress bool) {
	s.calls++
	s.bytes += int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		s.failures++
	}
	if end-start > s.maxCallEnd-s.maxCallStart {
		s.maxCallStart, s.maxCallEnd = start, end
	}
	if !progress {
		return
	}
	if !s.progress {
		s.first, s.progress = end, true
	} else if end-s.last > s.maxGapEnd-s.maxGapStart {
		s.maxGapStart, s.maxGapEnd = s.last, end
	}
	s.last = end
}

func (s streamTimingIO) snapshot() map[string]any {
	r := map[string]any{"calls": s.calls, "bytes": s.bytes, "errors": s.failures,
		"max_call_ms":       (s.maxCallEnd - s.maxCallStart).Milliseconds(),
		"max_call_start_ms": s.maxCallStart.Milliseconds(), "max_call_end_ms": s.maxCallEnd.Milliseconds()}
	if s.progress {
		r["first_ms"], r["last_ms"] = s.first.Milliseconds(), s.last.Milliseconds()
		r["max_gap_ms"] = (s.maxGapEnd - s.maxGapStart).Milliseconds()
		r["max_gap_start_ms"], r["max_gap_end_ms"] = s.maxGapStart.Milliseconds(), s.maxGapEnd.Milliseconds()
	}
	return r
}

// All fields below are protected by openAIForwardTiming.eventMu. No body,
// arbitrary event name, header value or error message is retained.
type openAIStreamTiming struct {
	io           [streamTimingIOCount]streamTimingIO
	events       [len(streamTimingEventNames)][3]int64 // count, first_ms, last_ms
	activity     [streamTimingBuckets][5]int64         // raw bytes, decoded bytes, SSE count, write bytes, flush count
	lastActivity time.Duration
	upstreamAt   time.Duration
	rawObserved  bool
	encoding     string
	protocol     string
	contentEvent string
	contentKind  string
}

func (t *openAIForwardTiming) streamElapsed() time.Duration {
	return t.offset + time.Since(t.started)
}

func (t *openAIForwardTiming) activityLocked(at time.Duration, column int, n int64) {
	index := int((at - t.offset) / streamTimingBucketWidth)
	if index < 0 {
		index = 0
	} else if index >= streamTimingBuckets {
		index = streamTimingBuckets - 1
	}
	t.stream.activity[index][column] += n
	if at > t.stream.lastActivity {
		t.stream.lastActivity = at
	}
}

func (t *openAIForwardTiming) recordStreamIO(kind int, start time.Duration, n int, err error) {
	end := t.streamElapsed()
	t.eventMu.Lock()
	defer t.eventMu.Unlock()
	t.stream.io[kind].record(start, end, n, err, n > 0 || kind == streamTimingFlush)
	column, count := kind, int64(n)
	if kind == streamTimingWrite {
		column = 3
	} else if kind == streamTimingFlush {
		column, count = 4, 1
	}
	if count > 0 {
		t.activityLocked(end, column, count)
	}
}

// ObserveOpenAIUpstreamBody belongs immediately before the repository's explicit
// decompression. This is an HTTP body boundary AFTER TLS/HTTP framing, not a
// packet capture. Go's transparent gzip decoding is explicitly labelled.
func ObserveOpenAIUpstreamBody(ctx context.Context, resp *http.Response) {
	t, _ := ctx.Value(openAIStreamTimingContextKey{}).(*openAIForwardTiming)
	if t == nil || resp == nil || resp.Body == nil {
		return
	}
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	switch encoding {
	case "", "identity":
		encoding = "identity"
	case "gzip", "br", "deflate", "zstd":
	default:
		encoding = "other"
	}
	if resp.Uncompressed {
		encoding = "transport_decoded"
	}
	protocol := "other"
	switch resp.Proto {
	case "HTTP/1.0", "HTTP/1.1", "HTTP/2.0", "HTTP/3.0":
		protocol = resp.Proto
	}
	t.eventMu.Lock()
	t.stream.rawObserved = true
	t.stream.upstreamAt = t.streamElapsed()
	t.stream.encoding, t.stream.protocol = encoding, protocol
	t.eventMu.Unlock()
	resp.Body = &openAITimingRawBody{ReadCloser: resp.Body, timing: t}
}

type openAITimingRawBody struct {
	io.ReadCloser
	timing *openAIForwardTiming
}

func (r *openAITimingRawBody) Read(p []byte) (int, error) {
	start := r.timing.streamElapsed()
	n, err := r.ReadCloser.Read(p)
	r.timing.recordStreamIO(streamTimingRaw, start, n, err)
	return n, err
}

func streamTimingEventIndex(eventType string) int {
	for i, name := range streamTimingEventNames {
		if eventType == name {
			return i
		}
	}
	return 0
}

func streamTimingContentKind(eventType, data string) string {
	switch {
	case strings.Contains(eventType, "reasoning"):
		return "reasoning"
	case strings.Contains(eventType, "function_call"), strings.Contains(eventType, "custom_tool"):
		return "tool"
	case strings.Contains(eventType, "output_text"), strings.Contains(eventType, "refusal"):
		return "text"
	case strings.Contains(eventType, "image_generation"):
		return "image"
	}
	switch gjson.Get(data, "item.type").String() {
	case "reasoning":
		return "reasoning"
	case "function_call", "custom_tool_call":
		return "tool"
	case "message":
		return "text"
	}
	return "other"
}

func (t *openAIForwardTiming) recordStreamEvent(eventType, data string, visible bool) {
	at := t.streamElapsed()
	index := streamTimingEventIndex(eventType)
	t.eventMu.Lock()
	defer t.eventMu.Unlock()
	event := &t.stream.events[index]
	if event[0] == 0 {
		event[1] = at.Milliseconds()
	}
	event[0]++
	event[2] = at.Milliseconds()
	t.activityLocked(at, 2, 1)
	if visible && t.stream.contentEvent == "" {
		t.stream.contentEvent = streamTimingEventNames[index]
		t.stream.contentKind = streamTimingContentKind(eventType, data)
	}
}

func (t *openAIForwardTiming) MarkSSEComment(line string) {
	if t != nil && strings.HasPrefix(line, ":") {
		t.recordStreamEvent("comment", "", false)
	}
}

// Wrap only the local streaming writer; do not replace Gin's pooled writer or
// change pre-output failover, heartbeat coordination or response commitment.
func (t *openAIForwardTiming) wrapWriter(w gin.ResponseWriter) gin.ResponseWriter {
	if t == nil {
		return w
	}
	return &openAITimingWriter{ResponseWriter: w, timing: t}
}

type openAITimingWriter struct {
	gin.ResponseWriter
	timing *openAIForwardTiming
}

func (w *openAITimingWriter) Write(p []byte) (int, error) {
	start := w.timing.streamElapsed()
	n, err := w.ResponseWriter.Write(p)
	w.timing.recordStreamIO(streamTimingWrite, start, n, err)
	return n, err
}

func (w *openAITimingWriter) WriteString(s string) (int, error) {
	start := w.timing.streamElapsed()
	n, err := w.ResponseWriter.WriteString(s)
	w.timing.recordStreamIO(streamTimingWrite, start, n, err)
	return n, err
}

func (w *openAITimingWriter) Flush() {
	start := w.timing.streamElapsed()
	w.ResponseWriter.Flush()
	w.timing.recordStreamIO(streamTimingFlush, start, 0, nil)
}

func (t *openAIForwardTiming) streamSnapshotLocked(row map[string]any) {
	if t.stream.rawObserved {
		row["upstream_headers_ms"] = t.stream.upstreamAt.Milliseconds()
		row["body_encoding"] = t.stream.encoding
		row["upstream_protocol"] = t.stream.protocol
	}
	ioStats := make(map[string]any)
	for i, name := range []string{"raw_body_read", "decoded_body_read", "downstream_write", "downstream_flush"} {
		if s := t.stream.io[i]; s.calls > 0 {
			ioStats[name] = s.snapshot()
		}
	}
	row["stream_io"] = ioStats
	events := make(map[string][3]int64)
	for i, event := range t.stream.events {
		if event[0] > 0 {
			events[streamTimingEventNames[i]] = event
		}
	}
	row["sse_events"] = events
	activity := make([][7]int64, 0, streamTimingBuckets)
	for i, counts := range t.stream.activity {
		if counts == [5]int64{} {
			continue
		}
		start := t.offset + time.Duration(i)*streamTimingBucketWidth
		end := start + streamTimingBucketWidth
		if i == streamTimingBuckets-1 && t.stream.lastActivity > end {
			end = t.stream.lastActivity
		}
		activity = append(activity, [7]int64{start.Milliseconds(), end.Milliseconds(), counts[0], counts[1], counts[2], counts[3], counts[4]})
	}
	row["stream_activity"] = activity
	if t.stream.contentEvent != "" {
		row["first_content_event_type"] = t.stream.contentEvent
		row["first_content_kind"] = t.stream.contentKind
	}
}
