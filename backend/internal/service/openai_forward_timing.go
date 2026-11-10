package service

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAIForwardTimingKey = "openai_forward_timing"
const openAISlowTimingKey = "openai_slow_timing"
const openAISlowTimingThreshold = 10 * time.Second

// Bounded per-request timestamps, never request/response bodies or headers.
// The first seven attempts and the latest attempt are retained on long retries.
type OpenAISlowTiming struct {
	started        time.Time
	handlerAt      time.Duration
	store          *openAISlowTimingStore
	attempts       [8]*openAIForwardTiming
	count          int
	routingAt      time.Duration
	userSlotAt     time.Duration
	selectionAt    time.Duration
	accountWaitAt  time.Duration
	accountSlotAt  time.Duration
	forwardAt      time.Duration
	firstForwardAt time.Duration
	forwardEndAt   time.Duration
	firstSemantic  atomic.Int64
	userID         int64
	apiKeyID       int64
	stream         bool
}

func (s *OpenAIGatewayService) BeginSlowTiming(c *gin.Context) *OpenAISlowTiming {
	if s == nil || s.slowTimingStore == nil || c == nil || c.Request == nil || c.Request.Method != http.MethodPost {
		return nil
	}
	now := time.Now()
	start, ok := c.Request.Context().Value(ctxkey.RequestStartedAt).(time.Time)
	if !ok {
		start = now
	}
	t := &OpenAISlowTiming{started: start, handlerAt: now.Sub(start), store: s.slowTimingStore}
	c.Set(openAISlowTimingKey, t)
	return t
}

func (t *OpenAISlowTiming) MarkRouting() {
	if t != nil {
		t.routingAt = time.Since(t.started)
	}
}
func (t *OpenAISlowTiming) MarkUserSlot() {
	if t != nil {
		t.userSlotAt = time.Since(t.started)
	}
}

func (t *OpenAISlowTiming) SetRequest(userID, apiKeyID int64, stream bool) {
	if t != nil {
		t.userID, t.apiKeyID, t.stream = userID, apiKeyID, stream
	}
}

func (t *OpenAISlowTiming) MarkSelection() {
	if t != nil {
		t.selectionAt = time.Since(t.started)
		t.accountWaitAt, t.accountSlotAt = 0, 0
	}
}
func (t *OpenAISlowTiming) MarkAccountWait() {
	if t != nil {
		t.accountWaitAt = time.Since(t.started)
	}
}
func (t *OpenAISlowTiming) MarkAccountSlot() {
	if t != nil {
		t.accountSlotAt = time.Since(t.started)
	}
}
func (t *OpenAISlowTiming) MarkForwardStart(c *gin.Context) {
	// Clear the previous account even when the next provider uses another path.
	c.Set(openAIForwardTimingKey, (*openAIForwardTiming)(nil))
	if t != nil {
		t.forwardAt = time.Since(t.started)
		if t.firstForwardAt == 0 {
			t.firstForwardAt = t.forwardAt
		}
	}
}
func (t *OpenAISlowTiming) MarkForwardEnd() {
	if t != nil {
		t.forwardEndAt = time.Since(t.started)
	}
}

func (t *OpenAISlowTiming) Finish(c *gin.Context) {
	if t == nil || !t.stream || t.count == 0 {
		return
	}
	now := time.Now()
	end := t.forwardEndAt
	outcome := "no_semantic_output"
	if n := t.firstSemantic.Load(); n > 0 {
		end = time.Duration(n - 1)
		outcome = "first_semantic_output"
	}
	wait := end - t.firstForwardAt
	if wait <= openAISlowTimingThreshold {
		return
	}
	rows := make([]map[string]any, 0, len(t.attempts))
	for _, attempt := range t.attempts {
		if attempt != nil {
			rows = append(rows, attempt.snapshot())
		}
	}
	ctx := c.Request.Context()
	requestID := slowTimingCorrelationID(ctx.Value(ctxkey.RequestID))
	clientID := slowTimingCorrelationID(ctx.Value(ctxkey.ClientRequestID))
	event := map[string]any{
		"time": now.UTC().Format(time.RFC3339Nano), "request_id": requestID, "client_request_id": clientID,
		"received_at": t.started.UTC().Format(time.RFC3339Nano), "handler_start_ms": t.handlerAt.Milliseconds(),
		"finished_ms": now.Sub(t.started).Milliseconds(), "status": c.Writer.Status(),
		"user_id": t.userID, "api_key_id": t.apiKeyID,
		"forward_start_ms": t.firstForwardAt.Milliseconds(), "forward_end_ms": t.forwardEndAt.Milliseconds(),
		"wait_ms": wait.Milliseconds(), "outcome": outcome,
		"attempt_count": t.count, "attempts": rows,
		"threshold_ms": openAISlowTimingThreshold.Milliseconds(), "threshold_scope": "forward_start_to_first_semantic",
		"measurement_boundary": "server_receive_to_local_flush",
	}
	if t.routingAt > 0 {
		event["routing_start_ms"] = t.routingAt.Milliseconds()
	}
	if t.userSlotAt > 0 {
		event["user_slot_acquired_ms"] = t.userSlotAt.Milliseconds()
	}
	if t.count > len(t.attempts) {
		event["omitted_attempts"] = t.count - len(t.attempts)
	}
	if v, ok := c.Get("ops_model"); ok {
		if model, ok := v.(string); ok && len(model) <= 128 && diagnosticSafeIdentifier(model) {
			event["model"] = model
		}
	}
	for _, key := range []string{OpsAuthLatencyMsKey, OpsRoutingLatencyMsKey} {
		if value, ok := c.Get(key); ok {
			if n, ok := value.(int64); ok {
				event[key] = n
			}
		}
	}
	t.store.write(now, event)
}

func slowTimingCorrelationID(value any) string {
	id, _ := value.(string)
	if diagnosticSafeIdentifier(id) {
		return id
	}
	return ""
}

// One immutable identity per attempt: callbacks never retain Gin's pooled context.
type openAIForwardTiming struct {
	started           time.Time
	offset            time.Duration
	accountID         int64
	attempt           int
	root              *OpenAISlowTiming
	selectionAt       time.Duration
	accountWaitAt     time.Duration
	accountSlotAt     time.Duration
	forwardAt         time.Duration
	status            atomic.Int64
	connection        atomic.Int64
	dnsStart          atomic.Int64
	dnsDone           atomic.Int64
	connectStart      atomic.Int64
	connectDone       atomic.Int64
	tlsStart          atomic.Int64
	tlsDone           atomic.Int64
	firstResponseByte atomic.Int64
	written           atomic.Int64
	headers           atomic.Int64
	body              atomic.Int64
	sse               atomic.Int64
	text              atomic.Int64
	semantic          atomic.Int64
	flushed           atomic.Int64
	closed            atomic.Int64
	eventMu           sync.Mutex
	event             string
}

func openAIForwardTimingFromContext(c *gin.Context) *openAIForwardTiming {
	if c == nil {
		return nil
	}
	v, _ := c.Get(openAIForwardTimingKey)
	t, _ := v.(*openAIForwardTiming)
	return t
}

func newOpenAIForwardTiming(c *gin.Context, request *http.Request, account *Account) *openAIForwardTiming {
	if c == nil {
		return nil
	}
	c.Set(openAIForwardTimingKey, (*openAIForwardTiming)(nil))
	if account == nil || account.Platform != PlatformOpenAI {
		return nil
	}
	v, _ := c.Get(openAISlowTimingKey)
	root, _ := v.(*OpenAISlowTiming)
	if root == nil || !root.stream {
		return nil
	}
	root.count++
	t := &openAIForwardTiming{started: time.Now(), accountID: account.ID, attempt: root.count, root: root,
		selectionAt: root.selectionAt, accountWaitAt: root.accountWaitAt, accountSlotAt: root.accountSlotAt, forwardAt: root.forwardAt}
	t.offset = t.started.Sub(root.started)
	index := root.count - 1
	if index >= len(root.attempts) {
		index = len(root.attempts) - 1
	}
	root.attempts[index] = t
	c.Set(openAIForwardTimingKey, t)
	return t
}

func (t *openAIForwardTiming) mark(value *atomic.Int64) {
	value.CompareAndSwap(0, time.Since(t.started).Nanoseconds()+1)
}
func (t *openAIForwardTiming) context(ctx context.Context) context.Context {
	if t == nil {
		return ctx
	}
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { t.mark(&t.dnsStart) },
		DNSDone:              func(httptrace.DNSDoneInfo) { t.mark(&t.dnsDone) },
		ConnectStart:         func(_, _ string) { t.mark(&t.connectStart) },
		ConnectDone:          func(_, _ string, _ error) { t.mark(&t.connectDone) },
		TLSHandshakeStart:    func() { t.mark(&t.tlsStart) },
		TLSHandshakeDone:     func(_ tls.ConnectionState, _ error) { t.mark(&t.tlsDone) },
		GotFirstResponseByte: func() { t.mark(&t.firstResponseByte) },
		GotConn:              func(httptrace.GotConnInfo) { t.mark(&t.connection) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				t.mark(&t.written)
			}
		},
	})
}

func (t *openAIForwardTiming) markResponse(response *http.Response, err error) {
	if t == nil {
		return
	}
	if response != nil {
		t.mark(&t.headers)
		t.status.Store(int64(response.StatusCode))
		if response.Body != nil {
			response.Body = &openAITimingBody{ReadCloser: response.Body, timing: t}
		}
	}
	if err != nil {
		t.mark(&t.closed)
	}
}

type openAITimingBody struct {
	io.ReadCloser
	timing *openAIForwardTiming
}

func (r *openAITimingBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.timing.mark(&r.timing.body)
	}
	return n, err
}
func (r *openAITimingBody) Close() error {
	err := r.ReadCloser.Close()
	r.timing.mark(&r.timing.closed)
	return err
}

func (t *openAIForwardTiming) MarkSSE(eventType, data string) {
	if t == nil {
		return
	}
	if t.sse.Load() == 0 {
		t.eventMu.Lock()
		if t.sse.Load() == 0 {
			t.event = "other"
			switch eventType {
			case "response.created", "response.in_progress", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.function_call_arguments.delta", "response.completed", "response.failed", "error":
				t.event = eventType
			}
			t.mark(&t.sse)
		}
		t.eventMu.Unlock()
	}
	if t.text.Load() == 0 && eventType == "response.output_text.delta" && gjson.Get(data, "delta").String() != "" {
		t.mark(&t.text)
	}
	// Metadata, empty deltas and error frames are not usable model output.
	// Include content-bearing terminal snapshots when an upstream omits deltas.
	if openAIStreamDataStartsVisibleOutput(data, eventType) {
		t.MarkSemantic()
	}
}

func (t *openAIForwardTiming) MarkSemantic() {
	if t != nil {
		t.mark(&t.semantic)
		if t.root != nil {
			t.root.firstSemantic.CompareAndSwap(0, int64(t.offset)+t.semantic.Load())
		}
	}
}

// Local writer boundary only; it does not measure arrival at the user's device.
func (t *openAIForwardTiming) MarkFlush() {
	if t != nil && t.semantic.Load() > 0 {
		t.mark(&t.flushed)
	}
}

func (t *openAIForwardTiming) snapshot() map[string]any {
	row := map[string]any{"account_id": t.accountID, "attempt": t.attempt, "start_ms": t.offset.Milliseconds(), "status": t.status.Load()}
	for name, value := range map[string]time.Duration{"selection_start_ms": t.selectionAt, "account_wait_start_ms": t.accountWaitAt, "account_slot_acquired_ms": t.accountSlotAt, "forward_start_ms": t.forwardAt} {
		if value > 0 {
			row[name] = value.Milliseconds()
		}
	}
	for name, value := range map[string]*atomic.Int64{
		"dns_start_ms": &t.dnsStart, "dns_done_ms": &t.dnsDone,
		"connect_start_ms": &t.connectStart, "connect_done_ms": &t.connectDone,
		"tls_start_ms": &t.tlsStart, "tls_done_ms": &t.tlsDone, "first_response_byte_ms": &t.firstResponseByte,
		"got_connection_ms": &t.connection, "request_written_ms": &t.written, "response_headers_ms": &t.headers,
		"first_body_byte_ms": &t.body, "first_sse_event_ms": &t.sse, "first_text_delta_ms": &t.text,
		"first_semantic_ms": &t.semantic, "first_downstream_flush_ms": &t.flushed, "body_closed_ms": &t.closed,
	} {
		if n := value.Load(); n > 0 {
			row[name] = (t.offset + time.Duration(n-1)).Milliseconds()
		}
	}
	t.eventMu.Lock()
	if t.event != "" {
		row["first_sse_event_type"] = t.event
	}
	t.eventMu.Unlock()
	return row
}
