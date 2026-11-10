package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// These regressions belong with the keepalive tests: diagnostic timestamps must
// never turn a heartbeat into model output or change the stream being forwarded.
func newSlowTimingTest(t *testing.T) (*OpenAIGatewayService, *gin.Context, *OpenAISlowTiming) {
	t.Helper()
	c, _ := newCompactBridgeTestContext(t, false)
	c.Request.URL.Path = "/v1/responses"
	ctx := context.WithValue(c.Request.Context(), ctxkey.RequestStartedAt, time.Now().Add(-20*time.Second))
	ctx = context.WithValue(ctx, ctxkey.RequestID, "request-123")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-456")
	c.Request = c.Request.WithContext(ctx)
	store := newOpenAISlowTimingStore(t.TempDir())
	t.Cleanup(store.Close)
	svc := &OpenAIGatewayService{cfg: &config.Config{}, slowTimingStore: store}
	root := svc.BeginSlowTiming(c)
	root.SetRequest(316, 12, true)
	root.firstForwardAt, root.forwardAt, root.forwardEndAt = time.Second, time.Second, 19*time.Second
	return svc, c, root
}

func readSlowTimingTest(t *testing.T, root *OpenAISlowTiming) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root.store.dir, "slow-*.jsonl"))
	require.NoError(t, err)
	var data strings.Builder
	for _, file := range files {
		body, err := os.ReadFile(file)
		require.NoError(t, err)
		data.Write(body)
	}
	return data.String()
}

func TestOpenAISlowTiming_ThresholdAndLongCompletion(t *testing.T) {
	for _, tc := range []struct {
		name           string
		wait           time.Duration
		noOutput, want bool
	}{
		{"fast", time.Second, false, false},
		{"exactly_ten", 10 * time.Second, false, false},
		{"over_ten", 10*time.Second + time.Nanosecond, false, true},
		{"fast_output_long_completion", 2 * time.Second, false, false},
		{"slow_without_output", 0, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, c, root := newSlowTimingTest(t)
			newOpenAIForwardTiming(c, c.Request, &Account{ID: 1, Platform: PlatformOpenAI})
			if !tc.noOutput {
				root.firstSemantic.Store(int64(root.firstForwardAt+tc.wait) + 1)
			}
			root.Finish(c)
			data := readSlowTimingTest(t, root)
			require.Equal(t, tc.want, data != "")
			if tc.want {
				require.True(t, gjson.Valid(strings.TrimSpace(data)))
				require.Equal(t, "request-123", gjson.Get(data, "request_id").String())
				require.Equal(t, int64(316), gjson.Get(data, "user_id").Int())
			}
		})
	}
}

func TestOpenAISlowTiming_RetriesAndBoundedIdentity(t *testing.T) {
	_, c, root := newSlowTimingTest(t)
	var first *openAIForwardTiming
	for i := 0; i < 12; i++ {
		attempt := newOpenAIForwardTiming(c, c.Request, &Account{ID: int64(i + 1), Platform: PlatformOpenAI})
		if first == nil {
			first = attempt
		}
		// No individual attempt waits ten seconds, but the overall chain does.
		attempt.offset = time.Duration(i+1) * time.Second
		attempt.closed.Store(int64(time.Second) + 1)
		if i == 11 {
			attempt.semantic.Store(int64(time.Second) + 1)
			attempt.MarkSemantic()
		}
	}
	trace := httptrace.ContextClientTrace(first.context(context.Background()))
	trace.GotConn(httptrace.GotConnInfo{})
	require.Positive(t, first.connection.Load())
	require.Zero(t, root.attempts[7].connection.Load(), "late callback must stay with its original attempt")
	root.Finish(c)
	data := readSlowTimingTest(t, root)
	require.Equal(t, int64(12000), gjson.Get(data, "wait_ms").Int())
	require.Equal(t, int64(12), gjson.Get(data, "attempt_count").Int())
	require.Equal(t, int64(4), gjson.Get(data, "omitted_attempts").Int())
	require.Len(t, gjson.Get(data, "attempts").Array(), 8)
	require.Equal(t, int64(12), gjson.Get(data, "attempts.7.account_id").Int())
	require.Nil(t, newOpenAIForwardTiming(c, c.Request, &Account{Platform: PlatformGrok}))
	require.Nil(t, openAIForwardTimingFromContext(c), "another provider must not inherit old timing")
}

func TestOpenAISlowTiming_MetadataPrivacyAndConcurrentCallbacks(t *testing.T) {
	_, c, root := newSlowTimingTest(t)
	a := newOpenAIForwardTiming(c, c.Request, &Account{ID: 9, Platform: PlatformOpenAI})
	a.MarkSSE("response.created", `{"type":"response.created"}`)
	a.MarkSSE("response.output_text.delta", `{"delta":""}`)
	a.MarkSSE("response.output_item.added", `{"item":{"type":"message","content":[]}}`)
	a.MarkSSE("error", `{"error":{"message":"secret-token"}}`)
	a.MarkFlush()
	require.Zero(t, a.semantic.Load())
	require.Zero(t, a.flushed.Load())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.MarkSSE("response.output_text.delta", `{"delta":"private-user-text"}`)
			a.MarkFlush()
			_ = a.snapshot()
		}()
	}
	wg.Wait()
	require.Positive(t, a.semantic.Load())
	require.Positive(t, a.text.Load())
	require.Positive(t, a.flushed.Load())
	root.Finish(c)
	data := readSlowTimingTest(t, root)
	require.NotEmpty(t, data)
	require.NotContains(t, data, "private-user-text")
	require.NotContains(t, data, "secret-token")
	require.Equal(t, "response.created", gjson.Get(data, "attempts.0.first_sse_event_type").String())
	require.Empty(t, slowTimingCorrelationID(strings.Repeat("x", 1024)))
	require.Empty(t, slowTimingCorrelationID("Bearer secret"))
	b := &openAIForwardTiming{started: time.Now()}
	b.MarkSSE("private-event-name", `{}`)
	require.Equal(t, "other", b.snapshot()["first_sse_event_type"])
	b.MarkSSE("response.completed", `{"response":{"output":[{"content":[{"text":"done-only"}]}]}}`)
	require.Positive(t, b.semantic.Load())
}

type slowTimingTestBody struct {
	io.Reader
	closed   bool
	closeErr error
}

func (b *slowTimingTestBody) Close() error { b.closed = true; return b.closeErr }

func TestOpenAISlowTiming_BodyTransparency(t *testing.T) {
	_, c, root := newSlowTimingTest(t)
	a := newOpenAIForwardTiming(c, c.Request, &Account{Platform: PlatformOpenAI})
	closeErr := errors.New("close sentinel")
	body := &slowTimingTestBody{Reader: strings.NewReader("\x00original\r\nbytes"), closeErr: closeErr}
	resp := &http.Response{StatusCode: 200, Body: body}
	a.markResponse(resp, nil)
	require.Zero(t, a.body.Load())
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "\x00original\r\nbytes", string(got))
	first := a.body.Load()
	require.Positive(t, first)
	_, err = resp.Body.Read(make([]byte, 1))
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, first, a.body.Load())
	require.ErrorIs(t, resp.Body.Close(), closeErr)
	require.True(t, body.closed)
	require.Positive(t, a.closed.Load())
	require.Equal(t, int64(200), a.status.Load())
	root.stream = false
	root.Finish(c)
	require.Empty(t, readSlowTimingTest(t, root), "nonstreaming requests have no measured streaming TTFT")
}

func TestOpenAISlowTiming_StreamingPaths(t *testing.T) {
	for _, path := range []string{"responses", "passthrough", "chat"} {
		t.Run(path, func(t *testing.T) {
			svc, c, root := newSlowTimingTest(t)
			root.MarkRouting()
			root.MarkUserSlot()
			root.MarkSelection()
			root.MarkAccountWait()
			root.MarkAccountSlot()
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
			a := newOpenAIForwardTiming(c, c.Request, account)
			body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"visible-output\",\"output_index\":0,\"content_index\":0}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
			resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
			a.markResponse(resp, nil)
			var err error
			switch path {
			case "responses":
				_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-test", "gpt-test")
			case "passthrough":
				_, err = svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, account, time.Now(), "gpt-test", "gpt-test")
			case "chat":
				_, err = svc.handleChatStreamingResponse(resp, c, account, "gpt-test", "gpt-test", "gpt-test", time.Now(), nil)
			}
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			root.MarkForwardEnd()
			root.Finish(c)
			data := readSlowTimingTest(t, root)
			for _, key := range []string{"response_headers_ms", "first_body_byte_ms", "first_sse_event_ms", "first_text_delta_ms", "first_semantic_ms", "first_downstream_flush_ms", "body_closed_ms", "account_slot_acquired_ms"} {
				require.True(t, gjson.Get(data, "attempts.0."+key).Exists(), key)
			}
			require.NotContains(t, data, "visible-output")
		})
	}
}

func TestOpenAISlowTiming_StoreCapRetentionAndIdleCleanup(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		limit  int64
	}{{"slow-", slowTimingBucketLimit}, {"error-", 32 << 10}} {
		t.Run(tc.prefix, func(t *testing.T) {
			s := newOpenAIBoundedDiagnosticStore(t.TempDir(), tc.prefix, tc.limit)
			t.Cleanup(s.Close)
			now := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
			for i := 0; i < 200; i++ {
				s.write(now, map[string]any{"padding": strings.Repeat("x", 1024)})
			}
			files, err := filepath.Glob(filepath.Join(s.dir, tc.prefix+"*.jsonl"))
			require.NoError(t, err)
			require.Len(t, files, 1)
			info, err := os.Stat(files[0])
			require.NoError(t, err)
			require.LessOrEqual(t, info.Size(), tc.limit)
			s.write(now.Add(time.Minute), map[string]any{"next": true})
			data, err := os.ReadFile(filepath.Join(s.dir, tc.prefix+now.Add(time.Minute).Format(slowTimingBucketLayout)+".jsonl"))
			require.NoError(t, err)
			require.Positive(t, gjson.GetBytes(data, "dropped_since_last_record").Int())
			unrelated := filepath.Join(s.dir, "keep.jsonl")
			require.NoError(t, os.WriteFile(unrelated, []byte("keep"), 0600))
			s.Close()
			// Controlled ticker proves cleanup runs without any new request writes.
			idle := &openAISlowTimingStore{dir: s.dir, prefix: tc.prefix, limit: tc.limit, stop: make(chan struct{}), done: make(chan struct{})}
			ticks := make(chan time.Time)
			go idle.runJanitor(ticks)
			t.Cleanup(idle.Close)
			ticks <- now.Add(2 * time.Hour)
			require.Eventually(t, func() bool { f, _ := filepath.Glob(filepath.Join(s.dir, tc.prefix+"*.jsonl")); return len(f) == 0 }, time.Second, time.Millisecond)
			require.FileExists(t, unrelated)
			idle.Close()
			idle.Close()
			idle.write(now, map[string]any{"after_close": true})
			files, err = filepath.Glob(filepath.Join(s.dir, tc.prefix+"*.jsonl"))
			require.NoError(t, err)
			require.Empty(t, files)
		})
	}
}

const keepaliveTestInterval = 10 * time.Millisecond

// waitForKeepaliveBeats 等待至少一次心跳写出。读取 recorder 前必须先经
// StopOpenAICompactSSEKeepaliveCommitted 停拍建立 happens-before。
func waitForKeepaliveBeats() {
	time.Sleep(20 * keepaliveTestInterval)
}

// stripKeepaliveComments 去掉 SSE 注释块，返回真实事件文本。
func stripKeepaliveComments(body string) string {
	var blocks []string
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		if strings.HasPrefix(strings.TrimSpace(block), ":") {
			continue
		}
		blocks = append(blocks, block)
	}
	return strings.Join(blocks, "\n\n")
}

func TestStartOpenAICompactSSEKeepalive_NoopWhenUnmarkedOrDisabled(t *testing.T) {
	// 未标记 client stream：不启动。
	c, rec := newCompactBridgeTestContext(t, false)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	waitForKeepaliveBeats()
	stop()
	require.Zero(t, rec.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))

	// interval=0（配置禁用）：不启动。
	c, rec = newCompactBridgeTestContext(t, true)
	stop = StartOpenAICompactSSEKeepalive(c, 0)
	waitForKeepaliveBeats()
	stop()
	require.Zero(t, rec.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}

func TestOpenAICompactSSEKeepalive_CommitsHeadersAndComments(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	require.Contains(t, rec.Body.String(), ": keepalive\n\n")
}

func TestOpenAICompactSSEKeepalive_StopBeforeFirstBeatKeepsWriterUntouched(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	stop()
	waitForKeepaliveBeats()
	require.Zero(t, rec.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}

func TestOpenAIAdjustedWrittenSizeExcludesResponsesStreamKeepalive(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, false)
	n, err := c.Writer.Write([]byte(":\n\n"))
	require.NoError(t, err)
	recordOpenAIStreamKeepaliveBytes(c, n)

	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c))

	_, err = c.Writer.Write([]byte("data: semantic\n\n"))
	require.NoError(t, err)
	require.Equal(t, len("data: semantic\n\n"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.Equal(t, ":\n\ndata: semantic\n\n", rec.Body.String())
}

// 心跳已提交后，2xx 桥接续写事件而不重复提交响应头。
func TestWriteOpenAICompactSSEBridge_AfterKeepaliveCommitAppendsEvents(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	finalResponse := []byte(`{"id":"resp_ka_1","output":[{"id":"cmp_ka","type":"compaction","encrypted_content":"x"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	require.True(t, writeOpenAICompactSSEBridge(c, http.StatusOK, finalResponse))

	require.Equal(t, http.StatusOK, rec.Code)
	events := parseCompactBridgeSSE(t, stripKeepaliveComments(rec.Body.String()))
	require.Len(t, events, 2)
	require.Equal(t, "response.output_item.done", events[0][0])
	require.Equal(t, "compaction", gjson.Get(events[0][1], "item.type").String())
	require.Equal(t, "response.completed", events[1][0])
	require.Equal(t, "resp_ka_1", gjson.Get(events[1][1], "response.id").String())
}

// 心跳已提交后上游非 2xx：状态码无法回传，必须以 response.failed 终止事件
// 收尾（Codex 将其作为终止事件处理），并标记流内错误供 ops 采集。
func TestWriteOpenAICompactSSEBridge_AfterKeepaliveCommitFailureEmitsFailedEvent(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	require.True(t, writeOpenAICompactSSEBridge(c, http.StatusBadGateway, []byte(`{"error":{"message":"upstream exploded"}}`)))

	events := parseCompactBridgeSSE(t, stripKeepaliveComments(rec.Body.String()))
	require.Len(t, events, 1)
	require.Equal(t, "response.failed", events[0][0])
	require.Equal(t, "failed", gjson.Get(events[0][1], "response.status").String())
	require.Contains(t, gjson.Get(events[0][1], "response.error.message").String(), "upstream exploded")
	require.NotEmpty(t, gjson.Get(events[0][1], "response.id").String())

	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, http.StatusBadGateway, streamErr.IntendedStatus)
}

// 心跳未提交时非 2xx 行为不变：返回 false，调用方按原 JSON+状态码写回。
func TestWriteOpenAICompactSSEBridge_BeforeKeepaliveCommitFailureKeepsJSONPath(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	stop()

	require.False(t, writeOpenAICompactSSEBridge(c, http.StatusBadGateway, []byte(`{"error":{"message":"fast fail"}}`)))
	require.Zero(t, rec.Body.Len())
}

// 未被显式拦截的写回路径（直接操作 c.Writer）也必须与心跳互斥：包装器在
// 请求侧任何响应构造时停拍。-race 下验证无数据竞争，且停拍后不再有心跳
// 字节写出。
func TestOpenAICompactKeepaliveWriter_RequestSideWriteSuspendsBeats(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	// 模拟未拦截路径的直接写回（如 Forward 内部本地拒绝的 c.JSON）。
	_, err := c.Writer.Write([]byte(`{"error":"local reject"}`))
	require.NoError(t, err)

	lenAfterWrite := rec.Body.Len()
	waitForKeepaliveBeats()
	require.Equal(t, lenAfterWrite, rec.Body.Len(), "请求侧写回后心跳必须停止")
	require.Contains(t, rec.Body.String(), ": keepalive\n\n")
	require.Contains(t, rec.Body.String(), `{"error":"local reject"}`)
}

func TestOpenAICompactKeepaliveWriter_NilInnerWriter_NoPanic(t *testing.T) {
	w := &openAICompactKeepaliveWriter{
		k: &openAICompactSSEKeepalive{stop: make(chan struct{})},
	}
	w.ResponseWriter = nil

	assert.NotPanics(t, func() {
		assert.Equal(t, 0, w.Status())
	})
	assert.NotPanics(t, func() {
		assert.Equal(t, 0, w.Size())
	})
	assert.NotPanics(t, func() {
		assert.False(t, w.Written())
	})
	assert.NotPanics(t, func() {
		assert.NotNil(t, w.Header())
	})
	assert.NotPanics(t, func() {
		n, err := w.Write([]byte("test"))
		assert.Equal(t, 0, n)
		assert.NoError(t, err)
	})
	assert.NotPanics(t, func() {
		n, err := w.WriteString("test")
		assert.Equal(t, 0, n)
		assert.NoError(t, err)
	})
	assert.NotPanics(t, func() {
		w.WriteHeader(http.StatusOK)
	})
	assert.NotPanics(t, func() {
		w.WriteHeaderNow()
	})
	assert.NotPanics(t, func() {
		w.Flush()
	})
	assert.NotPanics(t, func() {
		conn, rw, err := w.Hijack()
		assert.Nil(t, conn)
		assert.Nil(t, rw)
		assert.Error(t, err)
	})
	assert.NotPanics(t, func() {
		ch := w.CloseNotify()
		assert.NotNil(t, ch)
	})
	assert.NotPanics(t, func() {
		assert.Nil(t, w.Pusher())
	})
}

func TestOpenAICompactKeepaliveWriter_NilKeepalive_NoPanic(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	w := &openAICompactKeepaliveWriter{ResponseWriter: c.Writer}

	assert.NotPanics(t, func() {
		assert.Equal(t, 0, w.Status())
	})
	assert.NotPanics(t, func() {
		assert.Equal(t, 0, w.Size())
	})
	assert.NotPanics(t, func() {
		assert.False(t, w.Written())
	})
	assert.NotPanics(t, func() {
		w.Header().Set("X-Test", "ok")
	})
	assert.NotPanics(t, func() {
		w.WriteHeader(http.StatusAccepted)
	})
	assert.NotPanics(t, func() {
		n, err := w.WriteString("ok")
		assert.Equal(t, 2, n)
		assert.NoError(t, err)
	})
	assert.NotPanics(t, func() {
		w.Flush()
	})
	require.Equal(t, "ok", rec.Header().Get("X-Test"))
	require.Equal(t, "ok", rec.Body.String())
}

func TestOpenAICompactKeepaliveWriter_DelegatesWhenReady(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	defer stop()

	w, ok := c.Writer.(*openAICompactKeepaliveWriter)
	require.True(t, ok)

	w.Header().Set("X-Test", "ok")
	w.WriteHeader(http.StatusAccepted)
	n, err := w.WriteString("ready")
	require.NoError(t, err)
	require.Equal(t, len("ready"), n)

	require.Equal(t, http.StatusAccepted, w.Status())
	require.Equal(t, len("ready"), w.Size())
	require.True(t, w.Written())
	require.Equal(t, "ok", rec.Header().Get("X-Test"))
	require.Equal(t, "ready", rec.Body.String())
}

// fast policy block 在心跳提交后必须降级为 response.failed 终止事件。
func TestWriteOpenAIFastPolicyBlockedResponse_AfterKeepaliveCommit(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	writeOpenAIFastPolicyBlockedResponse(c, &OpenAIFastBlockedError{Message: "tier blocked"})

	require.Equal(t, http.StatusOK, rec.Code)
	events := parseCompactBridgeSSE(t, stripKeepaliveComments(rec.Body.String()))
	require.Len(t, events, 1)
	require.Equal(t, "response.failed", events[0][0])
	require.Equal(t, "permission_error", gjson.Get(events[0][1], "response.error.code").String())
	require.Contains(t, gjson.Get(events[0][1], "response.error.message").String(), "tier blocked")
}

// failover"是否已写响应"判定的口径：心跳字节必须被排除，否则 compact 在
// 上游等待期间发过心跳后，可换号的 failover 会被误判放弃；真实响应字节
// 写出后口径必须变化。
func TestOpenAICompactKeepaliveAdjustedWrittenSize_ExcludesHeartbeatBytes(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	// 无心跳的请求：等价于 c.Writer.Size()。
	require.Equal(t, c.Writer.Size(), OpenAICompactKeepaliveAdjustedWrittenSize(c))

	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	before := OpenAICompactKeepaliveAdjustedWrittenSize(c)
	waitForKeepaliveBeats()
	require.Equal(t, before, OpenAICompactKeepaliveAdjustedWrittenSize(c), "仅心跳字节不得改变判定口径")

	// 真实响应字节写出（经包装器，先停拍再写）后口径必须变化。
	_, err := c.Writer.Write([]byte("real-bytes"))
	require.NoError(t, err)
	require.Equal(t, len("real-bytes"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.Contains(t, rec.Body.String(), ": keepalive\n\n")
}

func TestOpenAIStreamClientOutputStarted_IgnoresCompactKeepaliveBytes(t *testing.T) {
	c, _ := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, keepaliveTestInterval)
	defer stop()
	waitForKeepaliveBeats()

	require.True(t, c.Writer.Written())
	require.False(t, openAIStreamClientOutputStarted(c, false), "keepalive comments are not semantic output")

	_, err := c.Writer.Write([]byte("real-output"))
	require.NoError(t, err)
	require.True(t, openAIStreamClientOutputStarted(c, false))
}

// fast policy block 在心跳未提交时保持 403 JSON 原语义。
func TestWriteOpenAIFastPolicyBlockedResponse_BeforeKeepaliveCommit(t *testing.T) {
	c, rec := newCompactBridgeTestContext(t, true)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	defer stop()

	writeOpenAIFastPolicyBlockedResponse(c, &OpenAIFastBlockedError{Message: "tier blocked"})

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "permission_error", gjson.Get(rec.Body.String(), "error.type").String())
}
