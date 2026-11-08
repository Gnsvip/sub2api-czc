package service

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSSEQueueBudgetBackpressureAndCancellation(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{SSEQueueMaxBytes: 64}}
	budget := newSSEQueueBudget(cfg)
	done := make(chan struct{})
	require.True(t, budget.acquire(48, done))
	acquired := make(chan bool, 1)
	go func() { acquired <- budget.acquire(32, done) }()
	select {
	case <-acquired:
		t.Fatal("queue accepted more bytes than its budget")
	case <-time.After(20 * time.Millisecond):
	}
	budget.release(48)
	require.True(t, <-acquired)
	budget.release(32)

	// Large image frames may occupy the budget alone, without reducing the
	// configured per-line limit to the (much smaller) queue budget.
	require.True(t, budget.acquire(1024, done))
	require.Equal(t, int64(64), budget.used.Load())
	go func() { acquired <- budget.acquire(1, done) }()
	select {
	case <-acquired:
		t.Fatal("queued another payload alongside an oversized frame")
	case <-time.After(20 * time.Millisecond):
	}
	close(done)
	select {
	case ok := <-acquired:
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not release queue producer")
	}
	budget.release(1024)
	require.Zero(t, budget.used.Load())
	require.False(t, budget.acquire(1, done))
}

func TestBoundedSSESplitRejectsUndelimitedEvents(t *testing.T) {
	for _, tc := range []struct {
		name    string
		gateway config.GatewayConfig
		wire    string
	}{
		{"lines", config.GatewayConfig{SSEEventMaxLines: 4}, strings.Repeat(": keepalive\n", 5)},
		{"bytes", config.GatewayConfig{SSEEventMaxBytes: 20}, strings.Repeat("data: abcd\n", 3)},
		{"incomplete_line", config.GatewayConfig{SSEEventMaxBytes: 20}, "data: " + strings.Repeat("a", 32)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scanner := bufio.NewScanner(strings.NewReader(tc.wire))
			scanner.Split(newBoundedSSESplit(&config.Config{Gateway: tc.gateway}, 1<<20, nil))
			for scanner.Scan() {
			}
			require.ErrorIs(t, scanner.Err(), errSSETransportLimit)
			require.ErrorIs(t, scanner.Err(), bufio.ErrTooLong)
		})
	}
}

func TestBoundedSSESplitResetsAtEventBoundaryAndPreservesEOF(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{SSEEventMaxBytes: 11, SSEEventMaxLines: 1}}
	scanner := bufio.NewScanner(strings.NewReader("data: abcd\n\ndata: last"))
	scanner.Split(newBoundedSSESplit(cfg, 1<<20, nil))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	require.NoError(t, scanner.Err())
	require.Equal(t, []string{"data: abcd", "", "data: last"}, lines)
}

func TestBoundedSSELineReaderPreservesLargeImageFrame(t *testing.T) {
	payload := "data: {\"b64_json\":\"" + strings.Repeat("A", 128<<10) + "\"}\r\n"
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 256 << 10, SSEQueueMaxBytes: 1024}}
	reader := newBoundedSSELineReader(strings.NewReader(payload+"\r\n"+"data: [DONE]"), cfg)
	line, err := reader.ReadLine()
	require.NoError(t, err)
	require.Equal(t, payload, string(line))
	line, err = reader.ReadLine()
	require.NoError(t, err)
	require.Equal(t, "\r\n", string(line))
	line, err = reader.ReadLine()
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, "data: [DONE]", string(line))
}

func TestBoundedSSELineReaderRejectsOversizeWithoutTruncatedPrefix(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 4096, SSEEventMaxBytes: 8192}}
	reader := newBoundedSSELineReader(strings.NewReader("data: "+strings.Repeat("A", 64<<10)), cfg)
	line, err := reader.ReadLine()
	require.Nil(t, line)
	require.ErrorIs(t, err, errSSETransportLimit)
}

func TestSSETransportDefaultsAndFirstOutputGuardCompose(t *testing.T) {
	require.Equal(t, int64(defaultSSEQueueMaxBytes), newSSEQueueBudget(&config.Config{}).limit)
	limits := newSSEEventLimits(&config.Config{}, 1234)
	require.Equal(t, int64(1234), limits.maxBytes)
	require.Equal(t, defaultSSEEventMaxLines, limits.maxLines)
	innerErr := errors.New("pre-output guard")
	scanner := bufio.NewScanner(strings.NewReader("data: test\n"))
	scanner.Split(newBoundedSSESplit(nil, 1024, func([]byte, bool) (int, []byte, error) { return 0, nil, innerErr }))
	require.False(t, scanner.Scan())
	require.ErrorIs(t, scanner.Err(), innerErr)
}

func TestSSEConfiguredLargeFrameLimitRemainsAvailable(t *testing.T) {
	defaults := newBoundedSSELineReader(strings.NewReader(""), nil)
	require.Equal(t, 500<<20, defaults.maxLineSize)
	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 64 << 20}}
	configured := newBoundedSSELineReader(strings.NewReader(""), cfg)
	require.Equal(t, 64<<20, configured.maxLineSize)
	require.Equal(t, int64(64<<20), configured.limits.maxBytes)
}
