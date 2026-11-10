package service

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

func hasNativeCodexInputItemID(id string) bool {
	prefix, suffix, found := strings.Cut(id, "_")
	return found && prefix != "" && suffix != ""
}

// Fixed Codex bfdbf01 protocol/response_item_id.rs::is_prefixed and
// core/client.rs::prepare_response_items_for_request preserve received IDs with
// nonempty prefix and suffix. A complete opaque reasoning item carries its own
// state under store=false; it is not a bare server-side ID lookup. Missing or
// incomplete opaque state deliberately retains the legacy compatibility policy.
// The encrypted string stays opaque: this predicate does not try to decrypt it.
func keepOpenAIReasoningReplayID(id, encrypted, status any) bool {
	itemID, ok := id.(string)
	if !ok {
		return false
	}
	if !hasNativeCodexInputItemID(itemID) {
		return false
	}
	opaque, ok := encrypted.(string)
	if !ok || strings.TrimSpace(opaque) == "" {
		return false
	}
	if status == nil {
		return true
	}
	state, ok := status.(string)
	return ok && (state == "" || state == "completed")
}

// HTTP OAuth requests are stateless even when native input skips the legacy
// transform. Keep self-contained encrypted reasoning and tool identities; bare
// reasoning IDs/references cannot be resolved under the required store=false.
// This runs only at HTTP send boundaries, never on a live WS continuation.
func normalizeOpenAIOAuthHTTPReasoningReplay(body []byte, account *Account) ([]byte, bool, error) {
	if account == nil || !account.IsOpenAIOAuthLike() {
		return body, false, nil
	}
	root := parseRawJSONView(body)
	input := root.Get("input")
	if !input.IsArray() {
		return body, false, nil
	}
	// Match the gateway decoder's last-key-wins behavior before selecting an
	// item by type/identity. Duplicate keys must never corrupt a tool item.
	decodeFallback := func() ([]byte, bool, error) {
		var decoded map[string]any
		if err := decodeOpenAIJSONUseNumber(body, &decoded); err != nil {
			return body, false, err
		}
		canonical, err := marshalOpenAIUpstreamJSON(decoded)
		if err != nil {
			return body, false, err
		}
		normalized, changed, err := normalizeOpenAIOAuthHTTPReasoningReplay(canonical, account)
		if err != nil || !changed {
			return body, false, err
		}
		return restoreOpenAIUpstreamJSONTopLevelOrder(body, normalized), true, nil
	}
	if hasDuplicateJSONObjectKeys(root) {
		return decodeFallback()
	}
	items := make([]string, 0)
	changed := false
	fallback := false
	var normalizeErr error
	input.ForEach(func(_, item gjson.Result) bool {
		if item.IsObject() && hasDuplicateJSONObjectKeys(item) {
			fallback = true
			return false
		}
		typ := strings.TrimSpace(item.Get("type").String())
		if typ == "item_reference" && strings.HasPrefix(strings.TrimSpace(item.Get("id").String()), "rs_") {
			changed = true
			return true
		}
		if typ != "reasoning" {
			items = append(items, item.Raw)
			return true
		}
		stripID := item.Get("id").Exists() && !keepOpenAIReasoningReplayID(item.Get("id").Value(), item.Get("encrypted_content").Value(), item.Get("status").Value())
		addSummary := item.Get("summary").Type == gjson.Null
		if !stripID && !addSummary && !item.Get("call_id").Exists() {
			items = append(items, item.Raw)
			return true
		}
		var decoded map[string]any
		if err := decodeOpenAIJSONUseNumber([]byte(item.Raw), &decoded); err != nil {
			normalizeErr = err
			return false
		}
		if stripID {
			delete(decoded, "id")
		}
		delete(decoded, "call_id")
		if addSummary {
			decoded["summary"] = []any{}
		}
		encoded, err := marshalOpenAIUpstreamJSON(decoded)
		if err != nil {
			normalizeErr = err
			return false
		}
		items = append(items, string(encoded))
		changed = true
		return true
	})
	if fallback {
		return decodeFallback()
	}
	if normalizeErr != nil {
		return body, false, fmt.Errorf("normalize OAuth HTTP reasoning replay: %w", normalizeErr)
	}
	if !changed {
		return body, false, nil
	}
	return replaceOpenAIRawInput(body, input, items), true, nil
}
