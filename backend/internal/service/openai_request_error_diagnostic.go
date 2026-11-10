package service

import (
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// These errors lack actionable provider details. Record only fixed vocabulary,
// types and counts from the final outbound body. Never persist body hashes,
// prompts, tool names/results, encrypted content, headers or arbitrary values.
func (s *OpenAIGatewayService) recordOpenAIRequestErrorDiagnostic(c *gin.Context, account *Account, status int, requestBody, responseBody []byte) {
	if s == nil || s.requestErrorStore == nil || account == nil || !account.IsOpenAIOAuthLike() || c == nil || c.Request == nil {
		return
	}
	message := strings.TrimSpace(extractUpstreamErrorMessage(responseBody))
	category := ""
	switch {
	case status == http.StatusBadRequest && strings.EqualFold(message, "Bad Request"):
		category = "opaque_bad_request"
	case status == http.StatusNotFound && strings.Contains(message, "Items are not persisted"):
		category = "unpersisted_item"
	default:
		return
	}
	const recordedKey = "openai_request_error_diagnostic_recorded"
	if c.GetBool(recordedKey) {
		return
	}
	c.Set(recordedKey, true)
	now := time.Now()
	ctx := c.Request.Context()
	event := map[string]any{
		"time":              now.UTC().Format(time.RFC3339Nano),
		"request_id":        slowTimingCorrelationID(ctx.Value(ctxkey.RequestID)),
		"client_request_id": slowTimingCorrelationID(ctx.Value(ctxkey.ClientRequestID)),
		"account_id":        account.ID, "upstream_status": status, "category": category,
		"passthrough": c.GetBool("openai_passthrough"),
		"request":     summarizeOpenAIRequestErrorBody(requestBody),
	}
	s.requestErrorStore.write(now, event)
}

func requestDiagnosticJSONType(value gjson.Result) string {
	switch value.Type {
	case gjson.Null:
		return "null"
	case gjson.True, gjson.False:
		return "boolean"
	case gjson.Number:
		return "number"
	case gjson.String:
		return "string"
	default:
		if value.IsArray() {
			return "array"
		}
		return "object"
	}
}

func requestDiagnosticItemType(value string) string {
	switch value {
	case "message", "reasoning", "item_reference", "function_call", "function_call_output",
		"custom_tool_call", "custom_tool_call_output", "tool_search_call", "tool_search_output",
		"web_search_call", "image_generation_call", "compaction", "compaction_summary",
		"function", "custom", "namespace", "web_search", "web_search_preview", "image_generation":
		return value
	default:
		return "other"
	}
}

func requestDiagnosticOption(value gjson.Result, allowed ...string) string {
	if !value.Exists() {
		return "absent"
	}
	if value.Type == gjson.String {
		for _, option := range allowed {
			if value.Str == option {
				return option
			}
		}
	}
	return "other"
}

func summarizeOpenAIRequestErrorBody(body []byte) map[string]any {
	root := parseRawJSONView(body)
	fields := make(map[string]string)
	for _, key := range []string{"model", "input", "instructions", "tools", "tool_choice", "stream", "store", "reasoning", "text", "include", "service_tier", "previous_response_id", "max_output_tokens", "parallel_tool_calls", "truncation", "metadata", "prompt_cache_key"} {
		if value := root.Get(key); value.Exists() {
			fields[key] = requestDiagnosticJSONType(value)
		}
	}
	inputTypes, toolTypes := make(map[string]int), make(map[string]int)
	inputCount, bareReasoning, reasoningIDs, missingSummary, reasoningReferences, missingCallIDs := 0, 0, 0, 0, 0, 0
	if input := root.Get("input"); input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			inputCount++
			typ := requestDiagnosticItemType(item.Get("type").String())
			inputTypes[typ]++
			if typ == "reasoning" {
				if encrypted := item.Get("encrypted_content"); encrypted.Type != gjson.String || strings.TrimSpace(encrypted.Str) == "" {
					bareReasoning++
				}
				if item.Get("id").Exists() {
					reasoningIDs++
				}
				if item.Get("summary").Type == gjson.Null {
					missingSummary++
				}
			}
			if typ == "item_reference" && strings.HasPrefix(item.Get("id").String(), "rs_") {
				reasoningReferences++
			}
			if (typ == "function_call_output" || typ == "custom_tool_call_output") && strings.TrimSpace(item.Get("call_id").String()) == "" {
				missingCallIDs++
			}
			return true
		})
	}
	if tools := root.Get("tools"); tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			toolTypes[requestDiagnosticItemType(tool.Get("type").String())]++
			return true
		})
	}
	return map[string]any{
		"body_bytes": len(body), "field_types": fields, "input_items": inputCount,
		"store_false": root.Get("store").Type == gjson.False, "stream_true": root.Get("stream").Type == gjson.True,
		"input_types": inputTypes, "tool_types": toolTypes,
		"bare_reasoning_items": bareReasoning, "reasoning_items_with_id": reasoningIDs,
		"reasoning_missing_summary": missingSummary, "reasoning_references": reasoningReferences,
		"tool_outputs_missing_call_id": missingCallIDs,
		"effort":                       requestDiagnosticOption(root.Get("reasoning.effort"), "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"),
		"service_tier":                 requestDiagnosticOption(root.Get("service_tier"), "auto", "default", "flex", "priority", "scale"),
		"text_format":                  requestDiagnosticOption(root.Get("text.format.type"), "text", "json_object", "json_schema"),
	}
}
