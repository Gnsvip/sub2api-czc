package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeReasoningReplayIdentityCompleteOpaqueAndLegacyBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		id, encrypted, status any
		keep                  bool
	}{
		{"complete", "rs_native", "opaque", "completed", true},
		{"optional_status", "rs_native", "opaque", nil, true},
		{"future_prefix", "future_native", "opaque", nil, true},
		{"long_server_id", "rs_" + strings.Repeat("a", 80), "opaque", nil, true},
		{"legacy", "legacy", "opaque", nil, false},
		{"empty_prefix", "_suffix", "opaque", nil, false},
		{"empty_suffix", "rs_", "opaque", nil, false},
		{"bare", "rs_native", nil, nil, false},
		{"empty_cipher", "rs_native", "   ", nil, false},
		{"invalid_cipher_type", "rs_native", 42, nil, false},
		{"incomplete", "rs_native", "opaque", "incomplete", false},
		{"failed", "rs_native", "opaque", "failed", false},
		{"in_progress", "rs_native", "opaque", "in_progress", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.keep, keepOpenAIReasoningReplayID(tc.id, tc.encrypted, tc.status))
			item := map[string]any{"type": "reasoning", "id": tc.id, "encrypted_content": tc.encrypted, "status": tc.status, "summary": []any{}}
			for _, preserve := range []bool{false, true} {
				got := filterCodexInput([]any{item}, preserve)[0].(map[string]any)
				_, hasID := got["id"]
				require.Equal(t, tc.keep, hasID)
				require.Equal(t, tc.id, item["id"], "caller input must not be mutated")
			}
			body, _ := json.Marshal(map[string]any{"store": false, "input": []any{item}})
			httpBody, _, err := normalizeOpenAIOAuthHTTPReasoningReplay(body, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth})
			require.NoError(t, err)
			require.Equal(t, tc.keep, gjson.GetBytes(httpBody, "input.0.id").Exists())
			require.Equal(t, gjson.GetBytes(body, "input.0.encrypted_content").Raw, gjson.GetBytes(httpBody, "input.0.encrypted_content").Raw)
			for _, normalize := range []func([]byte, bool) ([]byte, bool, error){normalizeOpenAIAPIKeyStoreFalseReasoningReplay, normalizeOpenAIAPIKeyStoreFalseReasoningReplayDecoded} {
				out, _, err := normalize(body, false)
				require.NoError(t, err)
				require.Equal(t, tc.keep, gjson.GetBytes(out, "input.0.id").Exists())
			}
		})
	}
}

func TestNativeReasoningReplayHTTPRepairScopesAndSummary(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_bare","call_id":"remove","summary":null,"extension":9007199254740993},{"type":"item_reference","id":"rs_missing"},{"type":"item_reference","id":"fc_keep"},{"type":"compaction","id":"cmp_keep","encrypted_content":"opaque"}]}`)
	for _, account := range []*Account{nil, {Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, {Platform: PlatformAnthropic, Type: AccountTypeOAuth}} {
		got, changed, err := normalizeOpenAIOAuthHTTPReasoningReplay(body, account)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, body, got)
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	got, changed, err := normalizeOpenAIOAuthHTTPReasoningReplay(body, account)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, int64(3), gjson.GetBytes(got, "input.#").Int())
	require.False(t, gjson.GetBytes(got, "input.0.id").Exists())
	require.False(t, gjson.GetBytes(got, "input.0.call_id").Exists())
	require.True(t, gjson.GetBytes(got, "input.0.summary").IsArray())
	require.Equal(t, "9007199254740993", gjson.GetBytes(got, "input.0.extension").Raw)
	require.Equal(t, "fc_keep", gjson.GetBytes(got, "input.1.id").String())
	require.Equal(t, gjson.GetBytes(body, "input.3").Raw, gjson.GetBytes(got, "input.2").Raw)
	again, changed, err := normalizeOpenAIOAuthHTTPReasoningReplay(got, account)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, got, again)
	duplicate := []byte(`{"input":[{"type":"reasoning","id":"rs_wrong"}],"input":[{"type":"reasoning","id":"rs_stale","encrypted_content":"opaque","encrypted_content":"","summary":[]},{"type":"reasoning","type":"function_call","id":"fc_keep","call_id":"call_keep","name":"lookup","arguments":"{}"}]}`)
	repaired, changed, err := normalizeOpenAIOAuthHTTPReasoningReplay(duplicate, account)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, int64(2), gjson.GetBytes(repaired, "input.#").Int())
	require.False(t, gjson.GetBytes(repaired, "input.0.id").Exists())
	require.Equal(t, "function_call", gjson.GetBytes(repaired, "input.1.type").String())
	require.Equal(t, "fc_keep", gjson.GetBytes(repaired, "input.1.id").String())
	require.Equal(t, "call_keep", gjson.GetBytes(repaired, "input.1.call_id").String())
}

func TestNativeReasoningReplayIdentityNativeForwardPreservesPrefixedOpaque(t *testing.T) {
	for _, kind := range []string{"oauth", "api_key"} {
		for _, id := range []string{"rs_native", "future_native", "rs_" + strings.Repeat("n", 80)} {
			t.Run(kind+"/"+id[:min(12, len(id))], func(t *testing.T) {
				body, _ := json.Marshal(map[string]any{"model": "gpt-5.5", "stream": false, "store": false, "instructions": "synthetic identity regression", "input": []any{
					map[string]any{"type": "reasoning", "id": id, "status": "completed", "summary": []any{}, "encrypted_content": "opaque-native-synthetic", "future": json.Number("9007199254740993")},
					map[string]any{"type": "message", "role": "user", "content": "continue synthetic fixture"},
				}})
				upstream := &httpUpstreamRecorder{responses: []*http.Response{newOpenAIRejectedFieldTestResponse(http.StatusOK, namespaceForwardOKResponse)}}
				account := newOpenAIRejectedFieldTestAccount()
				if kind == "oauth" {
					account = newOpenAIOAuthNamespaceTestAccount()
				}
				result, err := newOpenAIRejectedFieldTestService(upstream).Forward(context.Background(), newOpenAIRejectedFieldTestContext(body), account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, upstream.bodies, 1)
				var reasoning gjson.Result
				for _, item := range gjson.GetBytes(upstream.bodies[0], "input").Array() {
					if item.Get("type").String() == "reasoning" {
						reasoning = item
						break
					}
				}
				require.True(t, reasoning.Exists())
				require.Equal(t, id, reasoning.Get("id").String())
				require.Equal(t, "opaque-native-synthetic", reasoning.Get("encrypted_content").String())
				require.Equal(t, "9007199254740993", reasoning.Get("future").Raw)
			})
		}
	}
}
